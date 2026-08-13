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

package receiver_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/events"
	"github.com/geda/geda-transfer/core/outbox"
	"github.com/geda/geda-transfer/core/receiver"
)

const secondToken = "second-device-token"

type outboxListing struct {
	Items []struct {
		ID         string `json:"id"`
		Filename   string `json:"filename"`
		Size       int64  `json:"size"`
		SHA256     string `json:"sha256"`
		Kind       string `json:"kind"`
		CapturedAt string `json:"captured_at"`
		URL        string `json:"url"`
	} `json:"items"`
}

// queue writes a file, queues it for a device, and hashes it -- the state the
// phone finds when it asks what is waiting.
func (h *harness) queue(deviceID, name string, body []byte) outbox.Item {
	h.t.Helper()

	path := filepath.Join(h.t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		h.t.Fatal(err)
	}

	items, err := h.srv.Outbox().Add(h.t.Context(), deviceID, []string{path})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.srv.Outbox().HashPending(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
	return items[0]
}

func (h *harness) outboxRequest(method, path, token string, headers map[string]string) *http.Response {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), method, h.URL+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return h.do(req)
}

func (h *harness) listOutbox(token string) outboxListing {
	h.t.Helper()

	resp := h.outboxRequest(http.MethodGet, receiver.OutboxPath, token, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("GET /v1/outbox: status %d", resp.StatusCode)
	}

	var listing outboxListing
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		h.t.Fatal(err)
	}
	return listing
}

func randomBody(t *testing.T, n int) []byte {
	t.Helper()
	body := make([]byte, n)
	for i := range body {
		body[i] = byte((i*7 + 13) % 251)
	}
	return body
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestOutboxOffersOnlyThisDevicesFiles(t *testing.T) {
	h := newHarness(t)
	h.addDevice("dev-2", "Someone else's iPhone", secondToken)

	body := randomBody(t, 2048)
	item := h.queue("dev-1", "holiday.mp4", body)
	h.queue("dev-2", "not-yours.pdf", randomBody(t, 64))

	listing := h.listOutbox(testToken)
	if len(listing.Items) != 1 {
		t.Fatalf("offered %d items, want 1", len(listing.Items))
	}

	got := listing.Items[0]
	switch {
	case got.ID != item.ID:
		t.Errorf("id is %q, want %q", got.ID, item.ID)
	case got.Filename != "holiday.mp4":
		t.Errorf("filename is %q", got.Filename)
	case got.Size != int64(len(body)):
		t.Errorf("size is %d, want %d", got.Size, len(body))
	case got.SHA256 != digest(body):
		t.Errorf("digest is %q, want %q", got.SHA256, digest(body))
	case got.Kind != outbox.KindVideo:
		t.Errorf("kind is %q, want video", got.Kind)
	case got.URL != receiver.OutboxPath+"/"+item.ID:
		t.Errorf("url is %q", got.URL)
	case got.CapturedAt == "":
		t.Error("a video was offered with no capture date")
	}
}

func TestOutboxNeedsAToken(t *testing.T) {
	h := newHarness(t)
	item := h.queue("dev-1", "notes.pdf", randomBody(t, 32))

	for _, path := range []string{receiver.OutboxPath, receiver.OutboxPath + "/" + item.ID} {
		resp := h.outboxRequest(http.MethodGet, path, "", nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: status %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestOutboxItemIsInvisibleToAnotherDevice(t *testing.T) {
	h := newHarness(t)
	h.addDevice("dev-2", "Someone else's iPhone", secondToken)

	item := h.queue("dev-1", "private.zip", randomBody(t, 128))

	// 404 rather than 403: the difference between "not yours" and "does not
	// exist" is itself information about another person's files.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		resp := h.outboxRequest(method, receiver.OutboxPath+"/"+item.ID, secondToken, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s by the wrong device: status %d, want 404", method, resp.StatusCode)
		}
	}

	// ...and the wrong device's attempt did not retire it for the right one.
	if items := h.listOutbox(testToken).Items; len(items) != 1 {
		t.Errorf("the owning device is offered %d items, want 1", len(items))
	}
}

func TestOutboxServesTheBytesAndResumesByRange(t *testing.T) {
	h := newHarness(t)
	body := randomBody(t, 300_000)
	item := h.queue("dev-1", "archive.zip", body)

	url := receiver.OutboxPath + "/" + item.ID

	resp := h.outboxRequest(http.MethodGet, url, testToken, nil)
	whole, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !bytes.Equal(whole, body) {
		t.Fatal("the served bytes differ from the file")
	}
	if want := strconv.Quote(digest(body)); resp.Header.Get("ETag") != want {
		t.Errorf("ETag is %q, want %q", resp.Header.Get("ETag"), want)
	}

	// What a background URLSession does after the phone loses Wi-Fi: ask for
	// the rest, quoting the validator it was given.
	const cut = 123_456
	partial := h.outboxRequest(http.MethodGet, url, testToken, map[string]string{
		"Range":    "bytes=" + strconv.Itoa(cut) + "-",
		"If-Range": resp.Header.Get("ETag"),
	})
	tail, err := io.ReadAll(partial.Body)
	partial.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if partial.StatusCode != http.StatusPartialContent {
		t.Fatalf("range request: status %d, want 206", partial.StatusCode)
	}
	if !bytes.Equal(append(append([]byte{}, body[:cut]...), tail...), body) {
		t.Fatal("resuming by range did not reconstruct the file")
	}
}

func TestOutboxAcknowledgementRetiresTheItem(t *testing.T) {
	h := newHarness(t)
	item := h.queue("dev-1", "clip.mov", randomBody(t, 512))
	url := receiver.OutboxPath + "/" + item.ID

	resp := h.outboxRequest(http.MethodDelete, url, testToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: status %d, want 204", resp.StatusCode)
	}

	if items := h.listOutbox(testToken).Items; len(items) != 0 {
		t.Errorf("an acknowledged item is still on offer: %+v", items)
	}

	// A phone whose acknowledgement was lost sends it again.
	again := h.outboxRequest(http.MethodDelete, url, testToken, nil)
	again.Body.Close()
	if again.StatusCode != http.StatusNoContent {
		t.Errorf("second DELETE: status %d, want 204", again.StatusCode)
	}

	stored, err := h.srv.Outbox().Item(t.Context(), "dev-1", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != outbox.StateDelivered {
		t.Errorf("state is %q, want delivered", stored.State)
	}
}

func TestOutboxRefusesAFileThatChangedAfterQueueing(t *testing.T) {
	h := newHarness(t)
	body := randomBody(t, 4096)
	item := h.queue("dev-1", "moving-target.zip", body)

	if err := os.WriteFile(item.SourcePath, randomBody(t, 8192), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(item.SourcePath, later, later); err != nil {
		t.Fatal(err)
	}

	resp := h.outboxRequest(http.MethodGet, receiver.OutboxPath+"/"+item.ID, testToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status %d, want 410; a stale file must not be served under a digest it no longer matches", resp.StatusCode)
	}
}

func TestOutboxFetchIsReportedToTheLiveView(t *testing.T) {
	bus := events.NewBus()
	h := newHarness(t, func(cfg *receiver.Config) { cfg.Events = bus })

	stream, cancel := bus.Subscribe(64)
	defer cancel()

	body := randomBody(t, 200_000)
	item := h.queue("dev-1", "holiday.mov", body)

	resp := h.outboxRequest(http.MethodGet, receiver.OutboxPath+"/"+item.ID, testToken, nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	var started, finished bool
	deadline := time.After(2 * time.Second)
	for !(started && finished) {
		select {
		case e := <-stream:
			if e.Direction != events.DirectionOutbound {
				t.Fatalf("a collected file was reported as %q", e.Direction)
			}
			if e.Name != "holiday.mov" {
				t.Errorf("event names the file %q", e.Name)
			}
			switch e.Kind {
			case events.KindStarted:
				started = true
			case events.KindFinished:
				if e.Offset != int64(len(body)) {
					t.Errorf("finished at offset %d, want %d", e.Offset, len(body))
				}
				finished = true
			}
		case <-deadline:
			t.Fatalf("the live view never saw the collection (started=%v finished=%v)", started, finished)
		}
	}
}
