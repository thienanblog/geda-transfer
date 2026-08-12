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
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestLimiterCapsAnnouncesPerSource(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := newLimiter(AnnouncesPerSecond, clock.now)
	src := netip.MustParseAddr("192.168.11.99")

	for i := 0; i < AnnouncesPerSecond; i++ {
		if !l.allow(src) {
			t.Fatalf("announce %d refused inside the budget", i+1)
		}
	}
	if l.allow(src) {
		t.Fatal("a sixth announce in the same instant should be dropped")
	}

	clock.advance(time.Second)
	if !l.allow(src) {
		t.Fatal("budget did not refill after a second")
	}
}

func TestLimiterIsPerSource(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := newLimiter(AnnouncesPerSecond, clock.now)

	noisy := netip.MustParseAddr("192.168.11.99")
	for i := 0; i < AnnouncesPerSecond+3; i++ {
		l.allow(noisy)
	}

	// One flooding client must not silence discovery for everyone else on the
	// segment.
	quiet := netip.MustParseAddr("192.168.11.7")
	if !l.allow(quiet) {
		t.Fatal("a different source was refused because of an unrelated flood")
	}
}

func TestLimiterEvictsIdleSources(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := newLimiter(AnnouncesPerSecond, clock.now)

	l.allow(netip.MustParseAddr("192.168.11.99"))
	clock.advance(2 * idleEviction)
	l.evictIdle()

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) != 0 {
		t.Fatalf("%d sources still tracked after eviction", len(l.buckets))
	}
}

func TestLimiterBoundsMemoryUnderSpoofedSources(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := newLimiter(AnnouncesPerSecond, clock.now)

	// A source-spoofing flood must not be able to grow the table without
	// bound; forgetting history is cheap, running out of memory is not.
	for i := 0; i < maxTrackedSources*2; i++ {
		l.allow(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > maxTrackedSources {
		t.Fatalf("tracking %d sources, cap is %d", len(l.buckets), maxTrackedSources)
	}
}
