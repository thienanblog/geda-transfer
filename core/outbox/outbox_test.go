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

package outbox_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/outbox"
	"github.com/geda/geda-transfer/core/store"
)

const (
	phone = "phone-1"
	other = "phone-2"
)

func newQueue(t *testing.T) (*outbox.Queue, *store.DB, string) {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, id := range []string{phone, other} {
		pair(t, db, id, false)
	}

	return outbox.New(db, slog.New(slog.DiscardHandler)), db, dir
}

func pair(t *testing.T, db *store.DB, id string, revoked bool) {
	t.Helper()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	var revokedAt any
	if revoked {
		revokedAt = now
	}

	_, err := db.SQL().ExecContext(t.Context(), `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at, revoked_at)
		VALUES (?, ?, 'ios', 'pin', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET revoked_at = excluded.revoked_at`,
		id, id, "hash-"+id, now, revokedAt)
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, dir, name string, size int) string {
	t.Helper()

	path := filepath.Join(dir, name)
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func digestOf(t *testing.T, path string) string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestQueuedFileIsHashedAndOffered(t *testing.T) {
	q, _, dir := newQueue(t)
	path := write(t, dir, "holiday.mp4", 5000)

	queued, err := q.Add(t.Context(), phone, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("queued %d items, want 1", len(queued))
	}
	if queued[0].State != outbox.StatePending {
		t.Errorf("state is %q immediately after queueing, want pending", queued[0].State)
	}

	// Nothing is offered before it has a digest: a phone that downloaded it
	// would have nothing to check the bytes against.
	offered, err := q.Offer(t.Context(), phone)
	if err != nil {
		t.Fatal(err)
	}
	if len(offered) != 0 {
		t.Fatalf("an unhashed item was offered to the phone: %+v", offered)
	}

	if n, err := q.HashPending(t.Context()); err != nil || n != 1 {
		t.Fatalf("HashPending = %d, %v; want 1, nil", n, err)
	}

	offered, err = q.Offer(t.Context(), phone)
	if err != nil {
		t.Fatal(err)
	}
	if len(offered) != 1 {
		t.Fatalf("offered %d items after hashing, want 1", len(offered))
	}

	item := offered[0]
	if item.SHA256 != digestOf(t, path) {
		t.Errorf("digest is %q, want %q", item.SHA256, digestOf(t, path))
	}
	if item.Size != 5000 {
		t.Errorf("size is %d, want 5000", item.Size)
	}
	if item.Kind != outbox.KindVideo {
		t.Errorf("kind is %q, want video", item.Kind)
	}
	if item.Filename != "holiday.mp4" {
		t.Errorf("filename is %q", item.Filename)
	}
	if item.CapturedAt == nil {
		t.Error("a video was queued with no capture date; the Photo Library would file it under today")
	}
}

func TestAnItemBelongsToOneDeviceOnly(t *testing.T) {
	q, _, dir := newQueue(t)
	path := write(t, dir, "notes.pdf", 128)

	queued, err := q.Add(t.Context(), phone, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.HashPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	id := queued[0].ID

	// The other phone must not be able to tell the difference between an item
	// that is not its own and one that never existed.
	if _, err := q.Item(t.Context(), other, id); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("looking up another device's item gave %v, want ErrNotFound", err)
	}
	if _, _, err := q.Open(t.Context(), other, id); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("opening another device's item gave %v, want ErrNotFound", err)
	}
	if err := q.Deliver(t.Context(), other, id); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("acknowledging another device's item gave %v, want ErrNotFound", err)
	}

	offered, err := q.Offer(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	if len(offered) != 0 {
		t.Errorf("the other phone was offered %d items", len(offered))
	}
}

func TestServingChecksTheFileIsStillTheOneThatWasHashed(t *testing.T) {
	q, _, dir := newQueue(t)
	path := write(t, dir, "archive.zip", 4096)

	queued, err := q.Add(t.Context(), phone, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.HashPending(t.Context()); err != nil {
		t.Fatal(err)
	}

	f, _, err := q.Open(t.Context(), phone, queued[0].ID)
	if err != nil {
		t.Fatalf("an untouched file would not open: %v", err)
	}
	f.Close()

	// Rewrite it with different content and a later mtime, as an editor would.
	write(t, dir, "archive.zip", 8192)
	if err := os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := q.Open(t.Context(), phone, queued[0].ID); !errors.Is(err, outbox.ErrSourceChanged) {
		t.Fatalf("a modified file opened with %v, want ErrSourceChanged", err)
	}

	item, err := q.Item(t.Context(), phone, queued[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != outbox.StateFailed {
		t.Errorf("state is %q after the source changed, want failed", item.State)
	}
	if item.Error == "" {
		t.Error("the item failed without saying why")
	}
}

func TestAMissingSourceFailsItsItemAndNotTheQueue(t *testing.T) {
	q, _, dir := newQueue(t)
	gone := write(t, dir, "gone.jpg", 64)
	kept := write(t, dir, "kept.jpg", 64)

	if _, err := q.Add(t.Context(), phone, []string{gone, kept}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	if _, err := q.HashPending(t.Context()); err != nil {
		t.Fatalf("one unreadable file stopped the whole queue: %v", err)
	}

	items, err := q.List(t.Context(), phone)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]outbox.State{}
	for _, item := range items {
		states[item.Filename] = item.State
	}
	if states["gone.jpg"] != outbox.StateFailed {
		t.Errorf("the missing file is %q, want failed", states["gone.jpg"])
	}
	if states["kept.jpg"] != outbox.StateReady {
		t.Errorf("the readable file is %q, want ready", states["kept.jpg"])
	}
}

func TestDeliveryIsIdempotent(t *testing.T) {
	q, _, dir := newQueue(t)
	path := write(t, dir, "clip.mov", 32)

	queued, err := q.Add(t.Context(), phone, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.HashPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	id := queued[0].ID

	if err := q.Claim(t.Context(), phone, id); err != nil {
		t.Fatal(err)
	}
	if err := q.Deliver(t.Context(), phone, id); err != nil {
		t.Fatal(err)
	}
	// A phone whose acknowledgement was lost sends it again; so does one that
	// is handed the same finished download after a relaunch.
	if err := q.Deliver(t.Context(), phone, id); err != nil {
		t.Fatalf("a second acknowledgement failed: %v", err)
	}

	item, err := q.Item(t.Context(), phone, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != outbox.StateDelivered {
		t.Errorf("state is %q, want delivered", item.State)
	}
	if item.DeliveredAt == nil {
		t.Error("a delivered item has no delivery time")
	}

	offered, err := q.Offer(t.Context(), phone)
	if err != nil {
		t.Fatal(err)
	}
	if len(offered) != 0 {
		t.Errorf("a delivered item is still on offer: %+v", offered)
	}
}

func TestQueueingRefusesWhatCannotBeSent(t *testing.T) {
	q, db, dir := newQueue(t)
	path := write(t, dir, "photo.heic", 16)

	if _, err := q.Add(t.Context(), phone, []string{dir}); err == nil {
		t.Error("queueing a folder was accepted")
	}
	if _, err := q.Add(t.Context(), phone, []string{filepath.Join(dir, "nope.jpg")}); err == nil {
		t.Error("queueing a file that does not exist was accepted")
	}
	if _, err := q.Add(t.Context(), "never-paired", []string{path}); err == nil {
		t.Error("queueing for an unknown device was accepted")
	}

	pair(t, db, other, true)
	if _, err := q.Add(t.Context(), other, []string{path}); err == nil {
		t.Error("queueing for an unpaired device was accepted; nothing would ever collect it")
	}
}

func TestRemoveAndClear(t *testing.T) {
	q, _, dir := newQueue(t)
	first := write(t, dir, "one.txt", 8)
	second := write(t, dir, "two.txt", 8)

	queued, err := q.Add(t.Context(), phone, []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.HashPending(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := q.Remove(t.Context(), other, queued[0].ID); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("removing another device's item gave %v", err)
	}
	if err := q.Remove(t.Context(), phone, queued[0].ID); err != nil {
		t.Fatal(err)
	}

	if err := q.Deliver(t.Context(), phone, queued[1].ID); err != nil {
		t.Fatal(err)
	}
	n, err := q.Clear(t.Context(), phone)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("cleared %d finished items, want 1", n)
	}

	waiting, err := q.Waiting(t.Context(), phone)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 0 {
		t.Errorf("%d items still waiting, want 0", len(waiting))
	}
}

func TestRunHashesWhatWasQueuedBeforeItStarted(t *testing.T) {
	q, _, dir := newQueue(t)
	path := write(t, dir, "left-over.jpg", 100)

	// A receiver stopped mid-hash comes back to a pending row that no Add will
	// ever wake.
	if _, err := q.Add(t.Context(), phone, []string{path}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go q.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		offered, err := q.Offer(t.Context(), phone)
		if err != nil {
			t.Fatal(err)
		}
		if len(offered) == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker never hashed a file queued before it started")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"IMG_0042.HEIC":     outbox.KindPhoto,
		"holiday.jpg":       outbox.KindPhoto,
		"raw.DNG":           outbox.KindPhoto,
		"clip.MOV":          outbox.KindVideo,
		"clip.mp4":          outbox.KindVideo,
		"archive.zip":       outbox.KindFile,
		"contract.pdf":      outbox.KindFile,
		"no-extension":      outbox.KindFile,
		"tricky.jpg.zip":    outbox.KindFile,
		"/tmp/deep/a.tiff":  outbox.KindPhoto,
		"backup.tar.gz":     outbox.KindFile,
		"screenshot.png":    outbox.KindPhoto,
		"presentation.pptx": outbox.KindFile,
	}
	for name, want := range cases {
		if got := outbox.Classify(name); got != want {
			t.Errorf("Classify(%q) = %q, want %q", name, got, want)
		}
	}
}

// A file that is gone is a dead item; a file that could not be opened for some
// other reason is very likely to open next time, and failing it would need a
// person to notice and queue it again.
func TestATransientOpenFailureDoesNotKillTheItem(t *testing.T) {
	q, _, dir := newQueue(t)
	path := write(t, dir, "locked.zip", 256)

	queued, err := q.Add(t.Context(), phone, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.HashPending(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Unreadable, but present -- which is a POSIX permission and nothing else.
	// On Windows os.Chmod only toggles the read-only attribute, so the file
	// opens anyway and there is no way to stage the condition; and root can
	// read it regardless of the mode. The receiver's behaviour under a
	// transient open error is the same everywhere, so the two platforms that
	// cannot produce one skip rather than assert something untrue.
	if runtime.GOOS == "windows" {
		t.Skip("chmod cannot make a file unreadable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; permissions do not apply")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if _, _, err := q.Open(t.Context(), phone, queued[0].ID); err == nil {
		t.Fatal("an unreadable file opened")
	}

	item, err := q.Item(t.Context(), phone, queued[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != outbox.StateReady {
		t.Errorf("state is %q after a transient open failure, want ready", item.State)
	}

	// Once it is readable again, it serves without anybody re-queueing it.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	f, _, err := q.Open(t.Context(), phone, queued[0].ID)
	if err != nil {
		t.Fatalf("the item did not recover: %v", err)
	}
	f.Close()
}
