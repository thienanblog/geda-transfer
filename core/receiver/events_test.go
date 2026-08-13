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
	"net/http"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/events"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/storage"
)

// watcher collects what a live transfer view would be shown.
//
// Everything it takes off the channel is kept, so a test can wait for the
// event that ends a transfer and then still inspect the progress that led up
// to it -- which is the interesting part and is published first.
type watcher struct {
	t    *testing.T
	ch   <-chan events.Event
	seen []events.Event
}

func watch(t *testing.T, bus *events.Bus) *watcher {
	t.Helper()
	ch, stop := bus.Subscribe(4096)
	t.Cleanup(stop)
	return &watcher{t: t, ch: ch}
}

// await returns the next event of the given kind, failing if none arrives.
func (w *watcher) await(kind events.Kind) events.Event {
	w.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-w.ch:
			w.seen = append(w.seen, e)
			if e.Kind == kind {
				return e
			}
		case <-deadline:
			w.t.Fatalf("no %q event arrived", kind)
		}
	}
}

// all returns every event received so far, including any still queued.
func (w *watcher) all() []events.Event {
	for {
		select {
		case e := <-w.ch:
			w.seen = append(w.seen, e)
		default:
			return w.seen
		}
	}
}

// ofKind filters what all() returned.
func (w *watcher) ofKind(kind events.Kind) []events.Event {
	var out []events.Event
	for _, e := range w.all() {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func withEvents(bus *events.Bus) func(*receiver.Config) {
	return func(c *receiver.Config) { c.Events = bus }
}

// What the desktop's live transfer view is built on: a file appears when it
// starts, moves while it is arriving, and resolves to a stored path.
func TestUploadPublishesItsLifecycle(t *testing.T) {
	bus := events.NewBus()
	h := newHarness(t, withEvents(bus))
	w := watch(t, bus)

	body := payload(3<<20, 7)
	d := digestOf(t, body)

	loc := h.create(len(body), map[string]string{
		"filename":    "IMG_4021.HEIC",
		"captured_at": "2026-07-04T15:09:03Z",
		"hash":        d.Full,
		"kind":        storage.KindPhoto,
	})

	started := w.await(events.KindStarted)
	if started.Name != "IMG_4021.HEIC" {
		t.Errorf("Name = %q, want IMG_4021.HEIC", started.Name)
	}
	if started.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", started.Size, len(body))
	}
	if started.AssetKind != storage.KindPhoto {
		t.Errorf("AssetKind = %q, want %q", started.AssetKind, storage.KindPhoto)
	}
	// The device is the authenticated one, not anything the client claimed.
	if started.DeviceID != "dev-1" || started.DeviceName != "An's iPhone" {
		t.Errorf("device = %q/%q, want dev-1/An's iPhone", started.DeviceID, started.DeviceName)
	}
	if started.UploadID == "" {
		t.Error("UploadID is empty")
	}

	resp := h.patch(loc, 0, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("patch: status %d", resp.StatusCode)
	}

	finished := w.await(events.KindFinished)
	if finished.UploadID != started.UploadID {
		t.Errorf("finish is for upload %q, start was %q", finished.UploadID, started.UploadID)
	}
	if finished.StoredPath != h.storedPath(resp) {
		t.Errorf("StoredPath = %q, want %q", finished.StoredPath, h.storedPath(resp))
	}
	if finished.Offset != int64(len(body)) {
		t.Errorf("Offset = %d, want %d", finished.Offset, len(body))
	}
	if finished.Deduplicated {
		t.Error("a first upload was reported as deduplicated")
	}
}

// One PATCH carries a whole file. Without progress reported from inside the
// copy, a 4K video would move from 0% to 100% in one step.
func TestProgressIsReportedDuringOneChunk(t *testing.T) {
	bus := events.NewBus()
	h := newHarness(t, withEvents(bus))
	w := watch(t, bus)

	// Big enough that the copy spans more than one progress interval on any
	// machine that can run the test suite at all.
	body := payload(24<<20, 11)

	loc := h.create(len(body), map[string]string{
		"filename": "VID_0007.MOV",
		"kind":     storage.KindVideo,
	})
	resp := h.patch(loc, 0, body)
	resp.Body.Close()

	w.await(events.KindFinished)

	progress := w.ofKind(events.KindProgress)
	if len(progress) == 0 {
		t.Fatal("a 24 MiB upload produced no progress events")
	}

	var last int64
	for _, e := range progress {
		if e.Offset <= last {
			t.Fatalf("progress went backwards: %d after %d", e.Offset, last)
		}
		if e.Offset > int64(len(body)) {
			t.Fatalf("progress offset %d exceeds the file's %d", e.Offset, len(body))
		}
		last = e.Offset
	}
	if last != int64(len(body)) {
		t.Errorf("the last progress event was at %d, want the full %d", last, len(body))
	}
}

// A resumed upload reaches a receiver that may have restarted, so it must
// still announce itself -- and its progress must be absolute, not restarted
// from zero, or the bar would jump backwards mid-transfer.
func TestResumeAnnouncesAndReportsAbsoluteProgress(t *testing.T) {
	bus := events.NewBus()
	h := newHarness(t, withEvents(bus))
	w := watch(t, bus)

	body := payload(256<<10, 13)
	const cut = 100 << 10

	loc := h.create(len(body), map[string]string{
		"filename": "VID_0008.MOV",
		"kind":     storage.KindVideo,
	})
	first := w.await(events.KindStarted)

	h.patch(loc, 0, body[:cut]).Body.Close()
	h.patch(loc, cut, body[cut:]).Body.Close()

	finished := w.await(events.KindFinished)
	if finished.UploadID != first.UploadID {
		t.Fatalf("finished a different upload: %q vs %q", finished.UploadID, first.UploadID)
	}

	// The property that matters is that the bar never jumps backwards. The
	// second chunk starts at `cut`, so a resume that restarted its count at
	// zero would show up here as a drop.
	var last int64
	for _, e := range w.ofKind(events.KindProgress) {
		if e.Offset < last {
			t.Fatalf("progress went backwards across the resume: %d after %d", e.Offset, last)
		}
		last = e.Offset
	}
	if last != int64(len(body)) {
		t.Errorf("the last progress event was at %d, want the full %d", last, len(body))
	}
}

// A live view that shows a row which never resolves reads to the user as a
// transfer still in flight. Every ending has to be published, including the
// ones that are not successes.
func TestChecksumMismatchIsPublishedAsAFailure(t *testing.T) {
	bus := events.NewBus()
	h := newHarness(t, withEvents(bus))
	w := watch(t, bus)

	body := payload(8<<10, 17)

	loc := h.create(len(body), map[string]string{
		"filename": "IMG_9999.HEIC",
		"kind":     storage.KindPhoto,
		// A hash the bytes do not have.
		"hash": "0000000000000000000000000000000000000000000000000000000000000000",
	})
	h.patch(loc, 0, body).Body.Close()

	failed := w.await(events.KindFailed)
	if failed.Name != "IMG_9999.HEIC" {
		t.Errorf("Name = %q, want IMG_9999.HEIC", failed.Name)
	}
	if failed.Error == "" {
		t.Error("the failure carries no reason")
	}
	if failed.StoredPath != "" {
		t.Errorf("a rejected upload reported a stored path: %q", failed.StoredPath)
	}
}

// An upload the client abandons with DELETE is finished, not merely paused.
func TestTerminatePublishesAFailure(t *testing.T) {
	bus := events.NewBus()
	h := newHarness(t, withEvents(bus))
	w := watch(t, bus)

	body := payload(8<<10, 19)
	loc := h.create(len(body), map[string]string{"filename": "IMG_1.HEIC"})
	w.await(events.KindStarted)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, loc, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Tus-Resumable", "1.0.0")
	h.do(req).Body.Close()

	if e := w.await(events.KindFailed); e.Error == "" {
		t.Error("a cancelled upload carries no reason")
	}
}

// Nobody watching is the normal case for gedad, and it must cost nothing and
// crash nothing.
func TestUploadWorksWithNoEventBus(t *testing.T) {
	h := newHarness(t)

	body := payload(8<<10, 23)
	loc := h.create(len(body), map[string]string{"filename": "IMG_2.HEIC"})

	resp := h.patch(loc, 0, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("patch: status %d", resp.StatusCode)
	}
	if h.storedPath(resp) == "" {
		t.Error("the file was not stored")
	}
}
