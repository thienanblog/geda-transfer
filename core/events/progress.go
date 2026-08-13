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

package events

import (
	"io"
	"time"
)

// MinProgressInterval is the shortest gap between two progress events for one
// upload.
//
// A single tus PATCH streams an entire file through one call, so without a
// clock the choice is between one event per file -- a progress bar that jumps
// from 0% to 100% on a 4K video -- and one per 32 KiB buffer, which is tens of
// thousands of events for that same video. Five updates a second is past what
// anybody can read and cheap enough to be invisible.
const MinProgressInterval = 200 * time.Millisecond

// Progress is an io.Writer that publishes KindProgress as bytes pass through
// it. It writes nothing anywhere; it is meant to sit in an io.MultiWriter
// alongside the file and the hasher.
//
// It is not safe for concurrent use, which matches its one caller: the bytes
// of a single upload arrive in order, on one goroutine.
type Progress struct {
	bus      *Bus
	base     Event
	interval time.Duration
	now      func() time.Time

	offset int64
	last   time.Time
}

// NewProgress starts a progress reporter for one upload.
//
// base supplies the fields every event of this upload shares -- the id, the
// device, the name, the declared size. offset is what the receiver already
// held, so a resumed upload reports absolute progress rather than restarting
// the bar at zero.
func NewProgress(bus *Bus, base Event, offset int64) *Progress {
	base.Kind = KindProgress
	base.At = time.Time{}
	return &Progress{
		bus:      bus,
		base:     base,
		interval: MinProgressInterval,
		now:      time.Now,
		offset:   offset,
	}
}

// Write counts b and publishes if the interval has elapsed.
//
// It always reports a full write and never an error: an io.MultiWriter treats
// a short write as a failure and aborts the copy, so a reporter that ever
// returned one would abort the transfer it is only supposed to be watching.
func (p *Progress) Write(b []byte) (int, error) {
	p.offset += int64(len(b))

	now := p.now()
	if now.Sub(p.last) >= p.interval {
		p.last = now
		p.publish(now)
	}
	return len(b), nil
}

// Offset is how many bytes have passed through, including the starting offset.
func (p *Progress) Offset() int64 { return p.offset }

// Flush publishes the current offset regardless of the interval. Call it when
// a chunk ends so the last partial interval is not lost.
func (p *Progress) Flush() {
	now := p.now()
	p.last = now
	p.publish(now)
}

func (p *Progress) publish(at time.Time) {
	e := p.base
	e.At = at
	e.Offset = p.offset
	p.bus.Publish(e)
}

var _ io.Writer = (*Progress)(nil)
