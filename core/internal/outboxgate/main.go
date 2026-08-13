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

// Command outboxgate drives the protocol half of the P7 gate.
//
//	"a 2GB ZIP lands in Files; a video lands in Photos, both verified"
//
// Where each file lands is a property of the phone, and no script can assert
// it. Everything up to that point is a property of this repository, and this
// is where it is checked: a real receiver over real TLS, a real pairing, a
// 2 GiB file queued and collected by a client that shares nothing with the
// receiver but a pinned key -- interrupted part way through, resumed by range,
// and verified against the digest the receiver published.
//
// It is a test harness, not the product. See scripts/verify-p7.sh.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/geda/geda-transfer/core/client"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dir  = flag.String("dir", "", "working directory (required)")
		size = flag.Int64("size", 2<<30, "size of the archive to queue, in bytes")
		cut  = flag.Float64("cut", 0.4, "fraction of the archive to take before the connection drops")
	)
	flag.Parse()

	if *dir == "" {
		return errors.New("-dir is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	svc, err := service.Open(ctx, service.Config{
		Name:     "Gate Mac",
		StateDir: filepath.Join(*dir, "state"),
		Dest:     filepath.Join(*dir, "Received"),
		Listen:   "127.0.0.1:0",
		// A gate must not need the product's fixed UDP port, or it cannot run
		// on a machine that is already receiving.
		Discovery: false,
		MDNS:      false,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		return err
	}
	defer svc.Close()

	serving := make(chan error, 1)
	go func() { serving <- svc.Run(ctx) }()
	defer func() {
		cancel()
		<-serving
	}()

	if err := waitFor(ctx, "the receiver to start", func() bool { return svc.Addr() != nil }); err != nil {
		return err
	}
	pass("a receiver is running on %s", svc.Addr())

	phone, err := pair(ctx, svc)
	if err != nil {
		return err
	}
	pass("a client paired with it over pinned TLS")

	// The two files the gate is about. The archive is what a person would
	// actually send between machines; the video is what has to reach a photo
	// library rather than a documents folder.
	archive, archiveDigest, err := makeFile(filepath.Join(*dir, "archive.zip"), *size)
	if err != nil {
		return err
	}
	video, videoDigest, err := makeFile(filepath.Join(*dir, "holiday.mov"), 8<<20)
	if err != nil {
		return err
	}
	pass("queued a %s archive and a %s video", human(*size), human(8<<20))

	if _, err := svc.Send(ctx, "phone-1", []string{archive, video}); err != nil {
		return err
	}

	// Nothing is offered until it has been hashed: a client that downloaded an
	// item without a digest would have nothing to check the bytes against.
	var items []client.OutboxItem
	err = waitFor(ctx, "the queued files to be hashed and offered", func() bool {
		items, err = phone.Outbox(ctx)
		return err == nil && len(items) == 2
	})
	if err != nil {
		return err
	}

	byName := map[string]client.OutboxItem{}
	for _, item := range items {
		byName[item.Filename] = item
	}

	// The kind is what decides where the file is allowed to land on the phone
	// (docs/PROTOCOL.md §6.4). Getting it wrong is the difference between a
	// video in the camera roll and a video nobody can find.
	if got := byName["holiday.mov"].Kind; got != "video" {
		return fmt.Errorf("the video was offered as kind %q; it would not reach the photo library", got)
	}
	if got := byName["archive.zip"].Kind; got != "file" {
		return fmt.Errorf("the archive was offered as kind %q; the photo library would refuse it", got)
	}
	pass("the video is offered as a video and the archive as a file")

	if byName["archive.zip"].SHA256 != archiveDigest {
		return fmt.Errorf("the archive's published digest is not the file's")
	}
	if byName["archive.zip"].Size != *size {
		return fmt.Errorf("the archive is offered as %d bytes, not %d", byName["archive.zip"].Size, *size)
	}
	pass("the digests published are the digests of the files on disk")

	// The video: straight through, verified, acknowledged. This is what a
	// phone does with anything small enough to finish in one go.
	if err := collect(ctx, phone, byName["holiday.mov"], videoDigest, 0); err != nil {
		return fmt.Errorf("the video: %w", err)
	}
	pass("the video arrived whole and its digest matched")

	// The archive: interrupted part way, resumed from a second request, and
	// verified over the join. This is the case a 2 GB file on a phone will
	// actually meet.
	interrupt := int64(float64(*size) * *cut)
	started := time.Now()
	if err := collect(ctx, phone, byName["archive.zip"], archiveDigest, interrupt); err != nil {
		return fmt.Errorf("the archive: %w", err)
	}
	elapsed := time.Since(started)
	pass("the archive survived an interruption at %s, resumed by range, and its digest matched",
		human(interrupt))
	pass("%s in %s (%s/s over loopback)", human(*size), elapsed.Round(time.Millisecond),
		human(int64(float64(*size)/elapsed.Seconds())))

	// Acknowledged means retired: the receiver stops offering it, and the
	// phone stops being asked to collect it again.
	remaining, err := phone.Outbox(ctx)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("%d acknowledged files are still on offer", len(remaining))
	}

	queued, err := svc.Outbox(ctx, "phone-1")
	if err != nil {
		return err
	}
	for _, item := range queued {
		if item.State != "delivered" {
			return fmt.Errorf("%s is recorded as %q, not delivered", item.Filename, item.State)
		}
	}
	pass("the receiver records both as delivered and offers nothing further")

	return nil
}

// collect fetches one item, optionally dropping the connection part way, and
// verifies the whole file against the digest the receiver published.
//
// The hash covers everything written, across both requests, which is the point
// of doing it this way: a resume that spliced the wrong tail on would produce a
// file of exactly the right length and the wrong contents.
func collect(
	ctx context.Context,
	c *client.Client,
	item client.OutboxItem,
	want string,
	interruptAt int64,
) error {
	hasher := sha256.New()
	var received int64

	if interruptAt > 0 {
		limited := &stopAfter{w: hasher, left: interruptAt}
		n, err := c.Fetch(ctx, item, limited, 0)
		if err == nil {
			return errors.New("the interrupted fetch reported success")
		}
		received = n
		if received != interruptAt {
			return fmt.Errorf("the interruption took %d bytes, expected %d", received, interruptAt)
		}
	}

	n, err := c.Fetch(ctx, item, hasher, received)
	if err != nil {
		return err
	}
	received += n

	if received != item.Size {
		return fmt.Errorf("collected %d bytes of %d", received, item.Size)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		return fmt.Errorf("the collected bytes hash to %s, not %s", got[:16], want[:16])
	}
	if got != item.SHA256 {
		return fmt.Errorf("the receiver published a digest the bytes do not match")
	}

	// Only now, and this order is the whole of the argument: a client that
	// acknowledged first would retire the receiver's copy on the strength of
	// bytes it had not checked (docs/DECISIONS.md).
	return c.AckOutbox(ctx, item.ID)
}

// stopAfter fails once it has taken n bytes, standing in for a phone that
// walked out of Wi-Fi range.
type stopAfter struct {
	w    io.Writer
	left int64
}

func (s *stopAfter) Write(p []byte) (int, error) {
	if int64(len(p)) >= s.left {
		n, err := s.w.Write(p[:s.left])
		s.left -= int64(n)
		if err != nil {
			return n, err
		}
		return n, errors.New("the connection dropped")
	}

	n, err := s.w.Write(p)
	s.left -= int64(n)
	return n, err
}

// pair does what a phone does with a QR code.
func pair(ctx context.Context, svc *service.Service) (*client.Client, error) {
	offer, err := svc.Pair(ctx, time.Minute)
	if err != nil {
		return nil, err
	}
	payload, err := pairing.Decode(offer.URI)
	if err != nil {
		return nil, err
	}
	// The offer advertises every interface; the gate listens on loopback.
	payload.Addrs = []string{svc.Addr().String()}

	c, _, err := client.PairWith(ctx, payload,
		client.Device{ID: "phone-1", Name: "Gate iPhone", Platform: "ios"},
		client.Config{})
	return c, err
}

// makeFile writes n bytes and returns the path and the digest.
//
// Random rather than zeroes: a filesystem that stores a run of zeroes as a
// hole would make the read side unrepresentatively fast, and this gate reports
// a throughput figure.
func makeFile(path string, n int64) (string, string, error) {
	f, err := os.Create(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	hasher := sha256.New()
	buf := make([]byte, 1<<20)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}

	for written := int64(0); written < n; {
		chunk := buf
		if remaining := n - written; remaining < int64(len(chunk)) {
			chunk = chunk[:remaining]
		}
		// Each megabyte differs from the last, so a fetch that repeated or
		// skipped a block could not still produce the right digest.
		chunk[0] = byte(written >> 20)
		chunk[1] = byte(written >> 28)

		if _, err := f.Write(chunk); err != nil {
			return "", "", err
		}
		hasher.Write(chunk)
		written += int64(len(chunk))
	}

	if err := f.Sync(); err != nil {
		return "", "", err
	}
	return path, hex.EncodeToString(hasher.Sum(nil)), nil
}

func waitFor(ctx context.Context, what string, ready func() bool) error {
	deadline := time.Now().Add(60 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", what)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}

func pass(format string, args ...any) {
	fmt.Printf("  ok: "+format+"\n", args...)
}

func human(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
