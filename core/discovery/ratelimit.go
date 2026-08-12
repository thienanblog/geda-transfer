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

package discovery

import (
	"net/netip"
	"sync"
	"time"
)

// limiter caps announces per source address (docs/PROTOCOL.md §2.2).
//
// The padding rule already makes reflection unprofitable; this bounds the work
// a single noisy or hostile source can make the receiver do, and keeps a
// misbehaving client from drowning out everyone else on the segment.
type limiter struct {
	rate  float64
	burst float64
	now   func() time.Time

	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxTrackedSources bounds the memory a spoofed-source flood can cost. When
// the table is full it is dropped wholesale rather than scanned for victims:
// the state is worth at most a fraction of a second of history, and rebuilding
// it costs nothing.
const maxTrackedSources = 4096

// idleEviction is how long a source is remembered after its last packet.
const idleEviction = time.Minute

func newLimiter(perSecond int, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{
		rate:    float64(perSecond),
		burst:   float64(perSecond),
		now:     now,
		buckets: make(map[netip.Addr]*bucket),
	}
}

// allow reports whether a reply to src is within budget.
func (l *limiter) allow(src netip.Addr) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[src]
	if !ok {
		if len(l.buckets) >= maxTrackedSources {
			l.buckets = make(map[netip.Addr]*bucket, maxTrackedSources)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[src] = b
	}

	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evictIdle drops sources that have gone quiet.
func (l *limiter) evictIdle() {
	cutoff := l.now().Add(-idleEviction)

	l.mu.Lock()
	defer l.mu.Unlock()

	for addr, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, addr)
		}
	}
}
