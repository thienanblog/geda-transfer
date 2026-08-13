// Copyright 2026 Geda
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/client"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/service"
)

// serve runs a receiver and pairs a client with it, the way a phone does.
//
// Everything the outbox is for happens over this connection: it is the same
// pinned TLS session, the same bearer token, and the same handlers a real
// device reaches.
func serve(t *testing.T) (*service.Service, *client.Client) {
	t.Helper()

	svc := open(t, func(c *service.Config) { c.Discovery = false; c.MDNS = false })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the receiver did not stop")
		}
	})

	deadline := time.After(10 * time.Second)
	for svc.Addr() == nil {
		select {
		case <-deadline:
			t.Fatal("the receiver never started")
		case <-time.After(5 * time.Millisecond):
		}
	}

	offer, err := svc.Pair(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := pairing.Decode(offer.URI)
	if err != nil {
		t.Fatal(err)
	}
	payload.Addrs = []string{svc.Addr().String()}

	c, _, err := client.PairWith(t.Context(), payload,
		client.Device{ID: "phone-1", Name: "An's iPhone", Platform: "ios"},
		client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return svc, c
}

func sum(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func bigBody(n int) []byte {
	body := make([]byte, n)
	for i := range body {
		body[i] = byte((i*31 + 7) % 253)
	}
	return body
}

// The P7 gate in miniature: a file queued on the receiver is collected by the
// device, survives an interruption, and is verified by hash before it is
// acknowledged.
func TestAQueuedFileIsCollectedResumedAndVerified(t *testing.T) {
	svc, c := serve(t)

	body := bigBody(1 << 20)
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	queued, err := svc.Send(t.Context(), "phone-1", []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Kind != "file" {
		t.Fatalf("queued %+v, want one item of kind file", queued)
	}

	// The worker hashes it. Until then the phone is told about nothing, so
	// that it can never download something it cannot verify.
	var items []client.OutboxItem
	waitFor(t, "the queued file to be offered", func() bool {
		items, err = c.Outbox(t.Context())
		return err == nil && len(items) == 1
	})

	item := items[0]
	if item.SHA256 != sum(body) {
		t.Fatalf("offered digest %q, want %q", item.SHA256, sum(body))
	}
	if item.Size != int64(len(body)) {
		t.Errorf("offered size %d, want %d", item.Size, len(body))
	}

	// Take part of it and stop, as a phone does when it leaves Wi-Fi.
	var got bytes.Buffer
	if _, err := c.Fetch(t.Context(), item, &limited{w: &got, left: 300_000}, 0); err == nil {
		t.Fatal("a truncated write reported success")
	}
	if got.Len() == 0 {
		t.Fatal("nothing arrived at all")
	}

	// A resume asks only for the remainder, and gets it.
	from := int64(got.Len())
	n, err := c.Fetch(t.Context(), item, &got, from)
	if err != nil {
		t.Fatalf("resuming from %d: %v", from, err)
	}
	if n != int64(len(body))-from {
		t.Errorf("the resume sent %d bytes, want %d", n, int64(len(body))-from)
	}
	if !bytes.Equal(got.Bytes(), body) {
		t.Fatal("the collected file differs from the source")
	}
	if sum(got.Bytes()) != item.SHA256 {
		t.Fatal("the digest the phone computed does not match the one it was given")
	}

	if err := c.AckOutbox(t.Context(), item.ID); err != nil {
		t.Fatal(err)
	}

	after, err := c.Outbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("an acknowledged file is still on offer: %+v", after)
	}

	queue, err := svc.Outbox(t.Context(), "phone-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].State != "delivered" {
		t.Fatalf("the receiver's record is %+v, want one delivered item", queue)
	}
	if queue[0].SourcePath != "" {
		t.Error("a listing leaked the sending machine's filesystem layout")
	}
}

func TestSendRefusesADeviceThatCannotCollect(t *testing.T) {
	svc, _ := serve(t)

	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Send(t.Context(), "never-paired", []string{path}); err == nil {
		t.Error("queueing for a device that has never paired was accepted")
	}
	if _, err := svc.Send(t.Context(), "phone-1", nil); err == nil {
		t.Error("queueing nothing was accepted")
	}

	if err := svc.Unpair(t.Context(), "phone-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(t.Context(), "phone-1", []string{path}); err == nil {
		t.Error("queueing for an unpaired device was accepted; nothing would ever collect it")
	}
}

func TestQueuedFilesAreCountedAgainstTheirDevice(t *testing.T) {
	svc, _ := serve(t)

	path := filepath.Join(t.TempDir(), "holiday.mov")
	if err := os.WriteFile(path, bigBody(2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(t.Context(), "phone-1", []string{path}); err != nil {
		t.Fatal(err)
	}

	devices, err := svc.Devices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("%d devices, want 1", len(devices))
	}
	if devices[0].Queued != 1 {
		t.Errorf("the device shows %d queued files, want 1; sending would look like it did nothing",
			devices[0].Queued)
	}
}

func TestCancellingAQueuedFileWithdrawsIt(t *testing.T) {
	svc, c := serve(t)

	path := filepath.Join(t.TempDir(), "mistake.pdf")
	if err := os.WriteFile(path, bigBody(64), 0o600); err != nil {
		t.Fatal(err)
	}
	queued, err := svc.Send(t.Context(), "phone-1", []string{path})
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the queued file to be offered", func() bool {
		items, err := c.Outbox(t.Context())
		return err == nil && len(items) == 1
	})

	if err := svc.CancelSend(t.Context(), "phone-1", queued[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.CancelSend(t.Context(), "phone-1", queued[0].ID); err == nil {
		t.Error("cancelling the same file twice was accepted")
	}

	items, err := c.Outbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("a withdrawn file is still on offer: %+v", items)
	}

	var apiErr *client.Error
	_, err = c.Fetch(t.Context(), client.OutboxItem{ID: queued[0].ID}, io.Discard, 0)
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Errorf("fetching a withdrawn file gave %v, want a 404", err)
	}
}

// limited fails once it has taken n bytes, standing in for a phone that lost
// its network part way through a download.
type limited struct {
	w    io.Writer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	if len(p) >= l.left {
		n, err := l.w.Write(p[:l.left])
		l.left -= n
		if err != nil {
			return n, err
		}
		return n, errors.New("the connection dropped")
	}

	n, err := l.w.Write(p)
	l.left -= n
	return n, err
}

func waitFor(t *testing.T, what string, ready func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
