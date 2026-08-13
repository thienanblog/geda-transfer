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
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/events"
)

var epoch = time.Date(2026, 7, 4, 15, 0, 0, 0, time.UTC)

func at(seconds float64) time.Time {
	return epoch.Add(time.Duration(seconds * float64(time.Second)))
}

func TestLifecycleBecomesOneRowThatResolves(t *testing.T) {
	l := newLive()

	l.apply(events.Event{
		Kind: events.KindStarted, At: at(0), UploadID: "u1",
		DeviceID: "d1", DeviceName: "An's iPhone",
		Name: "IMG_4021.HEIC", AssetKind: "photo", Size: 1000,
	})

	snapshot := l.snapshot(at(0))
	if len(snapshot.Active) != 1 {
		t.Fatalf("Active = %d, want 1", len(snapshot.Active))
	}
	if snapshot.Active[0].Name != "IMG_4021.HEIC" || snapshot.Active[0].Outcome != "" {
		t.Fatalf("unexpected row: %+v", snapshot.Active[0])
	}

	l.apply(events.Event{Kind: events.KindProgress, At: at(1), UploadID: "u1", Size: 1000, Offset: 400})
	if got := l.snapshot(at(1)).Active[0].Offset; got != 400 {
		t.Errorf("Offset = %d, want 400", got)
	}

	l.apply(events.Event{
		Kind: events.KindFinished, At: at(2), UploadID: "u1", Size: 1000, Offset: 1000,
		StoredPath: "An's iPhone/IMG_4021.HEIC",
	})

	snapshot = l.snapshot(at(2))
	if len(snapshot.Active) != 0 {
		t.Errorf("a finished transfer is still shown as arriving: %+v", snapshot.Active)
	}
	if len(snapshot.Recent) != 1 {
		t.Fatalf("Recent = %d, want 1", len(snapshot.Recent))
	}
	if snapshot.Recent[0].Outcome != OutcomeStored {
		t.Errorf("Outcome = %q, want %q", snapshot.Recent[0].Outcome, OutcomeStored)
	}
	if snapshot.Recent[0].StoredPath == "" {
		t.Error("the finished row has no path, so Show would have nothing to reveal")
	}
}

// A file the receiver already held is neither a success nor a failure, and
// saying so is the difference between "it worked" and "why is it not there".
func TestDeduplicatedIsReportedAsSkipped(t *testing.T) {
	l := newLive()
	l.apply(events.Event{Kind: events.KindStarted, At: at(0), UploadID: "u1", Size: 10})
	l.apply(events.Event{
		Kind: events.KindFinished, At: at(1), UploadID: "u1", Size: 10, Offset: 10,
		StoredPath: "IMG.HEIC", Deduplicated: true,
	})

	if got := l.snapshot(at(1)).Recent[0].Outcome; got != OutcomeSkipped {
		t.Errorf("Outcome = %q, want %q", got, OutcomeSkipped)
	}
}

func TestFailureCarriesItsReason(t *testing.T) {
	l := newLive()
	l.apply(events.Event{Kind: events.KindStarted, At: at(0), UploadID: "u1", Size: 10})
	l.apply(events.Event{
		Kind: events.KindFailed, At: at(1), UploadID: "u1", Error: "content does not match the declared hash",
	})

	row := l.snapshot(at(1)).Recent[0]
	if row.Outcome != OutcomeFailed {
		t.Errorf("Outcome = %q, want %q", row.Outcome, OutcomeFailed)
	}
	if row.Error == "" {
		t.Error("the failed row shows no reason")
	}
}

// The bus drops events when a subscriber falls behind. Losing a start must not
// lose the file: showing it late beats not showing it.
func TestProgressWithoutAStartStillAppears(t *testing.T) {
	l := newLive()
	l.apply(events.Event{
		Kind: events.KindProgress, At: at(0), UploadID: "u1",
		DeviceName: "An's iPhone", Name: "IMG_9.HEIC", Size: 100, Offset: 50,
	})

	snapshot := l.snapshot(at(0))
	if len(snapshot.Active) != 1 || snapshot.Active[0].Name != "IMG_9.HEIC" {
		t.Fatalf("the file was lost: %+v", snapshot.Active)
	}
}

func TestRateIsMeasuredOverTheWindow(t *testing.T) {
	l := newLive()
	l.apply(events.Event{Kind: events.KindStarted, At: at(0), UploadID: "u1", Size: 10_000_000})

	// Two megabytes a second for two seconds.
	for i := 1; i <= 2; i++ {
		l.apply(events.Event{
			Kind: events.KindProgress, At: at(float64(i)), UploadID: "u1",
			Size: 10_000_000, Offset: int64(i) * 2_000_000,
		})
	}

	rate := l.snapshot(at(2)).BytesPerSecond
	if rate < 1_900_000 || rate > 2_100_000 {
		t.Errorf("rate = %.0f B/s, want about 2 MB/s", rate)
	}
}

// A stalled transfer must not keep reporting the speed it had before it
// stalled: the window would show a healthy number over a dead connection.
func TestRateFallsToZeroWhenNothingArrives(t *testing.T) {
	l := newLive()
	l.apply(events.Event{Kind: events.KindStarted, At: at(0), UploadID: "u1", Size: 10_000_000})
	l.apply(events.Event{Kind: events.KindProgress, At: at(1), UploadID: "u1", Size: 10_000_000, Offset: 2_000_000})

	if rate := l.snapshot(at(60)).BytesPerSecond; rate != 0 {
		t.Errorf("rate = %.0f B/s long after the last byte, want 0", rate)
	}
}

// A resume re-reports an absolute offset. Counting the repeat as new bytes
// would show a burst of throughput that never happened.
func TestRepeatedOffsetsDoNotInflateTheRate(t *testing.T) {
	l := newLive()
	l.apply(events.Event{Kind: events.KindStarted, At: at(0), UploadID: "u1", Size: 1000})
	l.apply(events.Event{Kind: events.KindProgress, At: at(1), UploadID: "u1", Size: 1000, Offset: 500})
	l.apply(events.Event{Kind: events.KindProgress, At: at(2), UploadID: "u1", Size: 1000, Offset: 500})

	if got := l.snapshot(at(2)).Active[0].Offset; got != 500 {
		t.Errorf("Offset = %d, want 500", got)
	}
	if rate := l.snapshot(at(2)).BytesPerSecond; rate > 500 {
		t.Errorf("rate = %.0f B/s, inflated by a repeated offset", rate)
	}
}

// Rows must not shuffle while somebody is reading them.
func TestActiveRowsAreOldestFirst(t *testing.T) {
	l := newLive()
	for i, id := range []string{"c", "a", "b"} {
		l.apply(events.Event{
			Kind: events.KindStarted, At: at(float64(i)), UploadID: id, Size: 10,
		})
	}

	active := l.snapshot(at(3)).Active
	if len(active) != 3 {
		t.Fatalf("Active = %d, want 3", len(active))
	}
	if active[0].UploadID != "c" || active[2].UploadID != "b" {
		t.Errorf("order is %s, %s, %s; want c, a, b",
			active[0].UploadID, active[1].UploadID, active[2].UploadID)
	}
}

func TestRecentListIsBounded(t *testing.T) {
	l := newLive()
	for i := range recentLimit + 20 {
		id := string(rune('a' + i%26))
		l.apply(events.Event{Kind: events.KindStarted, At: at(float64(i)), UploadID: id, Size: 1})
		l.apply(events.Event{Kind: events.KindFinished, At: at(float64(i)), UploadID: id, Size: 1, Offset: 1})
	}

	if got := len(l.snapshot(at(999)).Recent); got != recentLimit {
		t.Errorf("Recent holds %d, want the cap of %d", got, recentLimit)
	}
}

// The pump exists so that eight files in flight do not become forty messages a
// second across the bridge.
func TestPumpCoalescesAndStopsWhenIdle(t *testing.T) {
	bus := events.NewBus()
	l := newLive()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	snapshots := make(chan Snapshot, 512)
	go l.pump(ctx, bus, func(s Snapshot) { snapshots <- s })

	// Give the subscription time to attach before publishing.
	deadline := time.Now().Add(5 * time.Second)
	for bus.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	bus.Publish(events.Event{Kind: events.KindStarted, UploadID: "u1", Size: 1000})
	for i := range 200 {
		bus.Publish(events.Event{
			Kind: events.KindProgress, UploadID: "u1", Size: 1000, Offset: int64(i) + 1,
		})
	}
	bus.Publish(events.Event{Kind: events.KindFinished, UploadID: "u1", Size: 1000, Offset: 1000})

	// One emit interval's worth of events must arrive as far fewer snapshots
	// than there were events.
	time.Sleep(4 * emitInterval)
	cancel()

	got := len(snapshots)
	if got == 0 {
		t.Fatal("the window was never told anything happened")
	}
	if got > 20 {
		t.Errorf("202 events produced %d snapshots; they are not being coalesced", got)
	}
}
