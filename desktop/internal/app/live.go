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

package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/geda/geda-transfer/core/events"
)

// Transfer is one file, as the live view shows it.
type Transfer struct {
	UploadID   string `json:"upload_id"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`

	// Name is the filename on the sending device. Untrusted -- the UI renders
	// it as text and never as markup or a path.
	Name string `json:"name"`
	Kind string `json:"kind"`

	Size   int64 `json:"size"`
	Offset int64 `json:"offset"`

	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`

	// Outcome is "" while running, then "stored", "skipped", or "failed".
	Outcome string `json:"outcome"`

	StoredPath string `json:"stored_path,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Outcomes a finished transfer can have.
const (
	OutcomeStored  = "stored"
	OutcomeSkipped = "skipped"
	OutcomeFailed  = "failed"
)

// Snapshot is what the window renders.
type Snapshot struct {
	// Active is what is arriving now, oldest first, so a row does not jump
	// around while the user is looking at it.
	Active []Transfer `json:"active"`

	// Recent is what just finished, newest first.
	Recent []Transfer `json:"recent"`

	// BytesPerSecond is measured over the last few seconds. Speed is the
	// headline feature, so the app shows what it is actually achieving rather
	// than only a percentage (AGENTS.md §5).
	BytesPerSecond float64 `json:"bytes_per_second"`

	// ActiveBytes and ActiveTotal are the totals across everything running,
	// which is what the one summary bar shows.
	ActiveBytes int64 `json:"active_bytes"`
	ActiveTotal int64 `json:"active_total"`

	UpdatedAt time.Time `json:"updated_at"`
}

// recentLimit is how many finished files the view keeps. Enough to see the
// tail of a batch; the rest is what History is for.
const recentLimit = 50

// rateWindow is how far back the throughput figure looks.
//
// Short enough to react when a transfer stalls, long enough not to swing wildly
// between two small files.
const rateWindow = 3 * time.Second

// emitInterval is the fastest the window is updated.
//
// The receiver publishes progress five times a second per file, and eight
// files can be in flight (AGENTS.md §3.2). Forwarding each one would put forty
// messages a second across the bridge to redraw a bar nobody can read that
// fast. Coalescing into one snapshot per tick keeps the bridge quiet, which is
// the same rule the phone follows: orchestration crosses, data does not
// (AGENTS.md §3.8).
const emitInterval = 100 * time.Millisecond

// live is the in-flight transfer table.
type live struct {
	mu     sync.Mutex
	active map[string]*Transfer
	recent []Transfer

	// samples holds (time, cumulative bytes) for the throughput figure.
	samples []sample

	// dirty is set by an event and cleared by the emitter, so a quiet
	// receiver produces no messages at all.
	dirty bool

	// cumulative counts every byte that has landed since the app started. The
	// rate is a difference of two of these, which is why it must never be
	// reset by a file finishing.
	cumulative int64
}

type sample struct {
	at    time.Time
	bytes int64
}

func newLive() *live {
	return &live{active: make(map[string]*Transfer)}
}

// apply folds one event into the table.
func (l *live) apply(e events.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.dirty = true

	switch e.Kind {
	case events.KindStarted:
		if _, ok := l.active[e.UploadID]; ok {
			return
		}
		l.active[e.UploadID] = &Transfer{
			UploadID:   e.UploadID,
			DeviceID:   e.DeviceID,
			DeviceName: e.DeviceName,
			Name:       e.Name,
			Kind:       e.AssetKind,
			Size:       e.Size,
			Offset:     e.Offset,
			StartedAt:  e.At,
		}

	case events.KindProgress:
		t, ok := l.active[e.UploadID]
		if !ok {
			// A progress event with no start is possible only if the start was
			// dropped by a full queue. Showing the file is better than losing
			// it, so the row is reconstructed from what the event carries.
			t = &Transfer{
				UploadID:   e.UploadID,
				DeviceID:   e.DeviceID,
				DeviceName: e.DeviceName,
				Name:       e.Name,
				Kind:       e.AssetKind,
				Size:       e.Size,
				StartedAt:  e.At,
			}
			l.active[e.UploadID] = t
		}
		l.advance(t, e.Offset, e.At)

	case events.KindFinished, events.KindFailed:
		t, ok := l.active[e.UploadID]
		if !ok {
			t = &Transfer{
				UploadID:   e.UploadID,
				DeviceID:   e.DeviceID,
				DeviceName: e.DeviceName,
				Name:       e.Name,
				Kind:       e.AssetKind,
				Size:       e.Size,
				StartedAt:  e.At,
			}
		}
		l.advance(t, e.Offset, e.At)
		delete(l.active, e.UploadID)

		t.EndedAt = e.At
		switch {
		case e.Kind == events.KindFailed:
			t.Outcome = OutcomeFailed
			t.Error = e.Error
		case e.Deduplicated:
			// Not a failure and not quite a store: the receiver already had
			// this exact content, so the bytes were discarded. Saying so is
			// the difference between "it worked" and "why is it not there".
			t.Outcome = OutcomeSkipped
			t.StoredPath = e.StoredPath
		default:
			t.Outcome = OutcomeStored
			t.StoredPath = e.StoredPath
		}

		l.recent = append([]Transfer{*t}, l.recent...)
		if len(l.recent) > recentLimit {
			l.recent = l.recent[:recentLimit]
		}
	}
}

// advance moves a transfer forward and records the bytes for the rate.
//
// Offsets are absolute and can repeat -- a resume re-reports where it is -- so
// only forward movement counts, or a reconnecting phone would show as a burst
// of throughput that never happened.
func (l *live) advance(t *Transfer, offset int64, at time.Time) {
	if offset > t.Offset {
		l.cumulative += offset - t.Offset
		t.Offset = offset
	}
	if at.IsZero() {
		at = time.Now()
	}
	l.samples = append(l.samples, sample{at: at, bytes: l.cumulative})
	l.trim(at)
}

// trim drops samples older than the rate window.
func (l *live) trim(now time.Time) {
	cutoff := now.Add(-rateWindow)
	keep := 0
	for keep < len(l.samples) && l.samples[keep].at.Before(cutoff) {
		keep++
	}
	// One sample before the cutoff is kept deliberately: the rate is the
	// difference across the window, and with only samples inside it the first
	// reading after a quiet period would be computed over a fraction of a
	// second and come out absurdly high.
	if keep > 0 {
		l.samples = l.samples[keep-1:]
	}
}

// snapshot renders the table. now is passed in so the rate is testable.
func (l *live) snapshot(now time.Time) Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshotLocked(now)
}

func (l *live) snapshotLocked(now time.Time) Snapshot {
	s := Snapshot{
		Active:    make([]Transfer, 0, len(l.active)),
		Recent:    append([]Transfer(nil), l.recent...),
		UpdatedAt: now,
	}
	for _, t := range l.active {
		s.Active = append(s.Active, *t)
		s.ActiveBytes += t.Offset
		s.ActiveTotal += t.Size
	}
	// Oldest first, so a finishing row leaves from the bottom and the rows
	// above it do not shuffle.
	sort.Slice(s.Active, func(i, j int) bool {
		if s.Active[i].StartedAt.Equal(s.Active[j].StartedAt) {
			return s.Active[i].UploadID < s.Active[j].UploadID
		}
		return s.Active[i].StartedAt.Before(s.Active[j].StartedAt)
	})

	l.trim(now)
	s.BytesPerSecond = l.rate(now)
	return s
}

// rate is the throughput over the window, or zero when nothing is moving.
func (l *live) rate(now time.Time) float64 {
	if len(l.samples) < 2 {
		return 0
	}
	first, last := l.samples[0], l.samples[len(l.samples)-1]

	// Nothing has landed recently: report zero rather than a stale average
	// that makes a stalled transfer look healthy.
	if now.Sub(last.at) > rateWindow {
		return 0
	}

	elapsed := last.at.Sub(first.at).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(last.bytes-first.bytes) / elapsed
}

// pump forwards the bus into the table and emits coalesced snapshots.
//
// It owns the only subscription, so the number of events crossing to the
// window is bounded by emitInterval no matter how many files are in flight.
func (l *live) pump(ctx context.Context, bus *events.Bus, emit func(Snapshot)) {
	ch, cancel := bus.Subscribe(0)
	defer cancel()

	ticker := time.NewTicker(emitInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case e, ok := <-ch:
			if !ok {
				return
			}
			l.apply(e)

		case now := <-ticker.C:
			l.mu.Lock()
			dirty := l.dirty
			l.dirty = false
			// After the last byte lands, the throughput figure still has to
			// decay to zero: without these extra ticks the window would keep
			// showing the speed of a transfer that ended minutes ago.
			settling := !dirty && (len(l.active) > 0 || l.rate(now) > 0)
			l.mu.Unlock()

			if !dirty && !settling {
				// A receiver with nothing arriving sends nothing at all,
				// rather than a heartbeat the window would have to ignore.
				continue
			}
			emit(l.snapshot(now))
		}
	}
}
