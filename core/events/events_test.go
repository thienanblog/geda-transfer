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
	"sync"
	"testing"
	"time"
)

func TestPublishReachesEverySubscriber(t *testing.T) {
	bus := NewBus()

	a, cancelA := bus.Subscribe(4)
	defer cancelA()
	b, cancelB := bus.Subscribe(4)
	defer cancelB()

	bus.Publish(Event{Kind: KindStarted, UploadID: "u1"})

	for i, ch := range []<-chan Event{a, b} {
		select {
		case e := <-ch:
			if e.UploadID != "u1" || e.Kind != KindStarted {
				t.Fatalf("subscriber %d got %+v", i, e)
			}
			if e.At.IsZero() {
				t.Errorf("subscriber %d: At was not stamped", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

// A subscriber that stops reading must not be able to block the code writing
// bytes to disk. That is the whole reason this package exists.
func TestPublishDoesNotBlockOnAFullSubscriber(t *testing.T) {
	bus := NewBus()
	_, cancel := bus.Subscribe(1)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			bus.Publish(Event{Kind: KindProgress})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that stopped reading")
	}

	if bus.Dropped() == 0 {
		t.Error("events were dropped but Dropped() reports none")
	}
}

func TestCancelClosesTheChannelAndIsIdempotent(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe(1)

	cancel()
	cancel() // must not panic on a double close

	if _, open := <-ch; open {
		t.Fatal("the channel was not closed")
	}
	if got := bus.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d after cancel, want 0", got)
	}

	// Publishing after every subscriber is gone must be a no-op, not a send
	// on a closed channel.
	bus.Publish(Event{Kind: KindFinished})
}

// Publish runs on the goroutine copying an upload to disk, while the UI
// subscribes and unsubscribes on its own. Nothing here may race.
func TestConcurrentPublishAndSubscribe(t *testing.T) {
	bus := NewBus()

	stop := make(chan struct{})

	var publishers sync.WaitGroup
	for range 4 {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					bus.Publish(Event{Kind: KindProgress})
				}
			}
		}()
	}

	var subscribers sync.WaitGroup
	for range 4 {
		subscribers.Add(1)
		go func() {
			defer subscribers.Done()
			for range 50 {
				ch, cancel := bus.Subscribe(2)
				<-ch
				cancel()
			}
		}()
	}

	subscribers.Wait()
	close(stop)
	publishers.Wait()
}

func TestNilBusIsUsable(t *testing.T) {
	var bus *Bus
	bus.Publish(Event{Kind: KindStarted})
	if bus.Dropped() != 0 || bus.Subscribers() != 0 {
		t.Fatal("a nil bus reported state")
	}
}

func TestProgressRateLimitsAndFlushes(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe(64)
	defer cancel()

	base := Event{UploadID: "u1", Name: "VID_0001.MOV", Size: 900}
	p := NewProgress(bus, base, 100)

	now := time.Unix(0, 0)
	p.now = func() time.Time { return now }
	p.last = now

	// Three writes inside one interval produce nothing...
	for range 3 {
		if n, err := p.Write(make([]byte, 100)); n != 100 || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	select {
	case e := <-ch:
		t.Fatalf("published inside the interval: %+v", e)
	default:
	}

	// ...and one after it does.
	now = now.Add(MinProgressInterval)
	p.Write(make([]byte, 100))

	e := <-ch
	if e.Kind != KindProgress {
		t.Errorf("Kind = %q, want %q", e.Kind, KindProgress)
	}
	// 100 starting offset plus four writes of 100.
	if e.Offset != 500 {
		t.Errorf("Offset = %d, want 500", e.Offset)
	}
	if e.UploadID != "u1" || e.Name != "VID_0001.MOV" || e.Size != 900 {
		t.Errorf("the base event was not carried through: %+v", e)
	}

	// Flush reports the tail the interval would otherwise swallow.
	p.Write(make([]byte, 400))
	p.Flush()
	if e := <-ch; e.Offset != 900 {
		t.Errorf("after Flush, Offset = %d, want 900", e.Offset)
	}
}

// io.MultiWriter aborts the copy on a short write, so a reporter that reported
// one would abort the transfer it is only watching.
func TestProgressAlwaysReportsAFullWrite(t *testing.T) {
	p := NewProgress(nil, Event{}, 0)
	n, err := p.Write(make([]byte, 4096))
	if n != 4096 || err != nil {
		t.Fatalf("Write = %d, %v; want 4096, nil", n, err)
	}
	if p.Offset() != 4096 {
		t.Fatalf("Offset() = %d, want 4096", p.Offset())
	}
}
