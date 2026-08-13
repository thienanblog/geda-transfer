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

// Package events carries what a receiver is doing right now to whoever is
// watching it.
//
// The ledger records what has finished. That is the wrong shape for a window
// showing a transfer in progress: by the time a row exists the interesting
// part is over. So the receiver publishes the lifecycle of each upload here,
// and the desktop app renders it.
//
// One rule governs the whole package: **a subscriber must never be able to
// slow a transfer down**. Publishing is non-blocking and a subscriber that
// falls behind loses events rather than applying back-pressure to the code
// writing bytes to disk. A dropped progress event costs a stale percentage
// for a fraction of a second; a blocked one would cost throughput, which is
// the headline feature (AGENTS.md §5).
package events

import (
	"sync"
	"sync/atomic"
	"time"
)

// Kind is what happened to an upload.
type Kind string

const (
	// KindStarted is published when a device creates an upload. For a resumed
	// upload it carries the offset the receiver already holds.
	KindStarted Kind = "started"

	// KindProgress reports bytes landing. It is rate-limited: see Throttle.
	KindProgress Kind = "progress"

	// KindFinished means the file is verified, committed, and durable.
	KindFinished Kind = "finished"

	// KindFailed means the upload will not produce a file. A transfer that is
	// merely interrupted does not produce this -- it can still be resumed --
	// so a failure here is a checksum mismatch, a full disk, or a client that
	// gave up and issued a DELETE.
	KindFailed Kind = "failed"
)

// Event is one thing that happened to one upload.
//
// Fields not relevant to the Kind are zero. Every event carries UploadID so a
// subscriber can key on it without tracking anything else.
type Event struct {
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`

	// UploadID identifies the upload for its whole life.
	UploadID string `json:"upload_id"`

	// DeviceID and DeviceName come from the authenticated session, never from
	// client metadata (docs/DECISIONS.md).
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`

	// Name is the filename on the sending device. Untrusted: a UI must treat
	// it as text, never as a path.
	Name string `json:"name"`

	// Kind of asset: photo, video, or file. Matches storage's constants.
	AssetKind string `json:"asset_kind,omitempty"`

	// Size is the declared total, and Offset how much has landed. Size is 0
	// when the client did not declare a length.
	Size   int64 `json:"size"`
	Offset int64 `json:"offset"`

	// StoredPath is the destination-relative path, set on KindFinished.
	StoredPath string `json:"stored_path,omitempty"`

	// Deduplicated is true when the receiver already held this content and
	// the bytes were discarded rather than stored a second time.
	Deduplicated bool `json:"deduplicated,omitempty"`

	// Error is a human-readable reason, set on KindFailed.
	Error string `json:"error,omitempty"`
}

// Bus fans events out to subscribers.
//
// The zero value is not usable; call NewBus. A nil *Bus is, though: Publish on
// one does nothing, so callers that have no watcher never need a branch.
type Bus struct {
	mu     sync.Mutex
	subs   map[int]chan Event
	nextID int

	dropped atomic.Uint64
}

// DefaultBuffer is the queue depth Subscribe uses when given zero.
//
// Deep enough to absorb a UI that is busy repainting, shallow enough that a
// subscriber which has genuinely stopped reading does not pin megabytes of
// events in memory for the life of the process.
const DefaultBuffer = 256

// NewBus returns an empty bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[int]chan Event)}
}

// Subscribe returns a channel of events and a function that stops the
// subscription. The channel is closed by that function and by nothing else,
// so a range over it ends exactly when the caller says so.
//
// Cancel is idempotent, which matters because the usual caller defers it and
// also calls it on a UI teardown path.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}

	ch := make(chan Event, buffer)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			// Closed under no lock but after removal, so Publish can no
			// longer reach this channel and cannot send on a closed one.
			close(ch)
		})
	}
}

// Publish delivers e to every current subscriber, dropping it for any whose
// queue is full. It never blocks and is safe on a nil Bus.
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			b.dropped.Add(1)
		}
	}
}

// Dropped counts events discarded because a subscriber was not keeping up.
// Exposed so the condition is observable rather than silent.
func (b *Bus) Dropped() uint64 {
	if b == nil {
		return 0
	}
	return b.dropped.Load()
}

// Subscribers reports how many channels are currently attached.
func (b *Bus) Subscribers() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
