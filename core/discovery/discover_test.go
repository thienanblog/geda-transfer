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
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

// testResponder starts a responder on an ephemeral loopback port.
//
// Loopback rather than the real discovery port so that tests never depend on a
// privileged port, never collide with a receiver running on the developer's
// machine, and never put packets on the office network.
func testResponder(t *testing.T, cfg ResponderConfig) (*Responder, int) {
	t.Helper()

	if cfg.Candidates == nil {
		cfg.Candidates = func() ([]string, error) {
			return []string{"127.0.0.1", "10.13.13.5"}, nil
		}
	}
	cfg.Logger = slog.New(slog.DiscardHandler)

	r, err := NewResponder(cfg)
	if err != nil {
		t.Fatal(err)
	}

	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go r.ServeConn(ctx, pc)

	return r, pc.LocalAddr().(*net.UDPAddr).Port
}

// TestUnicastSweepFindsAReceiver exercises L3 with every multicast layer
// switched off.
//
// This is the mechanism the cross-subnet gate depends on. mDNS is multicast
// with TTL=1 and a router drops it; a WireGuard tunnel has no broadcast domain
// at all. What is left is unicast, and this test proves unicast alone is
// enough to find a receiver.
func TestUnicastSweepFindsAReceiver(t *testing.T) {
	_, port := testResponder(t, ResponderConfig{
		DeviceID: "receiver-1", Name: "Studio Mac", Platform: "darwin",
		TransferPort: 47891, SPKI: "test-pin",
	})

	start := time.Now()
	r, err := First(t.Context(), Config{
		Port:             port,
		Subnets:          []netip.Prefix{netip.MustParsePrefix("127.0.0.0/24")},
		DisableBroadcast: true,
		DisableMDNS:      true,
		Timeout:          3 * time.Second,
		Logger:           slog.New(slog.DiscardHandler),
	}, func(r Result) bool { return r.DeviceID == "receiver-1" })
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("the sweep did not find the receiver: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("found the receiver after %s, the gate is 3s", elapsed)
	}
	if len(r.Sources) == 0 || r.Sources[0] != SourceSweep {
		t.Fatalf("sources = %v, want the unicast sweep", r.Sources)
	}
	t.Logf("unicast sweep of a /24 found the receiver in %s", elapsed)
}

// TestDiscoveryWorksInBothDirections is the shape of the phase gate: two peers
// that each run a responder find each other, neither one privileged.
func TestDiscoveryWorksInBothDirections(t *testing.T) {
	_, portA := testResponder(t, ResponderConfig{DeviceID: "peer-a", Name: "Peer A", TransferPort: 47891, SPKI: "pin-a"})
	_, portB := testResponder(t, ResponderConfig{DeviceID: "peer-b", Name: "Peer B", TransferPort: 47891, SPKI: "pin-b"})

	find := func(port int, want string) time.Duration {
		t.Helper()

		start := time.Now()
		_, err := First(t.Context(), Config{
			Port:             port,
			Manual:           []string{"127.0.0.1"},
			DisableBroadcast: true,
			DisableMDNS:      true,
			Timeout:          3 * time.Second,
			Logger:           slog.New(slog.DiscardHandler),
		}, func(r Result) bool { return r.DeviceID == want })
		if err != nil {
			t.Fatalf("%s was not found: %v", want, err)
		}
		return time.Since(start)
	}

	if d := find(portB, "peer-b"); d > 3*time.Second {
		t.Fatalf("A found B in %s", d)
	}
	if d := find(portA, "peer-a"); d > 3*time.Second {
		t.Fatalf("B found A in %s", d)
	}
}

func TestAnnounceCarriesTunnelAddresses(t *testing.T) {
	r, port := testResponder(t, ResponderConfig{
		DeviceID: "receiver-1", Name: "Studio Mac", TransferPort: 47891, SPKI: "test-pin",
		Candidates: func() ([]string, error) {
			return []string{"192.168.11.20", "10.13.13.5"}, nil
		},
	})
	_ = r

	results, err := Discover(t.Context(), Config{
		Port:             port,
		Manual:           []string{"127.0.0.1"},
		DisableBroadcast: true,
		DisableMDNS:      true,
		Timeout:          time.Second,
		Logger:           slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	// The tunnel address is the one that reaches this receiver from another
	// subnet. Dropping it from the candidate set is what breaks WireGuard.
	var sawTunnel bool
	for _, a := range results[0].Addrs {
		if a == "10.13.13.5" {
			sawTunnel = true
		}
	}
	if !sawTunnel {
		t.Fatalf("candidate set %v does not include the tunnel address", results[0].Addrs)
	}
}

func TestResponderIgnoresUnpaddedProbe(t *testing.T) {
	_, port := testResponder(t, ResponderConfig{DeviceID: "receiver-1", TransferPort: 47891})

	conn, err := net.Dial("udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	short, err := json.Marshal(Probe{V: Version, T: TypeProbe, Nonce: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(short); err != nil {
		t.Fatal(err)
	}

	// Answering an unpadded probe is what would make this an amplifier.
	if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, maxDatagram)
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("responder answered an unpadded probe with %d bytes", n)
	}
}

func TestResponderRateLimitsAnnounces(t *testing.T) {
	_, port := testResponder(t, ResponderConfig{DeviceID: "receiver-1", TransferPort: 47891})

	conn, err := net.Dial("udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	probe, err := MarshalProbe(nonce)
	if err != nil {
		t.Fatal(err)
	}

	const sent = 25
	for i := 0; i < sent; i++ {
		if _, err := conn.Write(probe); err != nil {
			t.Fatal(err)
		}
	}

	replies := 0
	buf := make([]byte, maxDatagram)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Read(buf); err != nil {
			break
		}
		replies++
	}

	// Five per second, plus whatever the bucket refilled during the read
	// window. A burst of 25 must not produce 25 replies.
	if replies == 0 {
		t.Fatal("no announce at all")
	}
	if replies > AnnouncesPerSecond+3 {
		t.Fatalf("%d announces for %d probes; the 5/s cap is not holding", replies, sent)
	}
}

func TestScanDiscardsAnnouncesWithTheWrongNonce(t *testing.T) {
	c := &collector{
		cfg:   Config{Logger: slog.New(slog.DiscardHandler)},
		nonce: "the-nonce-this-scan-sent",
		seen:  map[string]int{},
	}

	c.add(Announce{V: Version, T: TypeAnnounce, DeviceID: "spoofed", Port: 47891, Nonce: "some-other-nonce"},
		mustAddrPort("192.168.11.99:47890"))
	c.add(Announce{V: Version, T: TypeAnnounce, DeviceID: "unsolicited", Port: 47891},
		mustAddrPort("192.168.11.98:47890"))

	if got := c.results(); len(got) != 0 {
		t.Fatalf("accepted %d announces this scan never asked for", len(got))
	}

	c.cfg.AcceptUnsolicited = true
	c.add(Announce{V: Version, T: TypeAnnounce, DeviceID: "unsolicited", Port: 47891},
		mustAddrPort("192.168.11.98:47890"))
	if got := c.results(); len(got) != 1 {
		t.Fatalf("got %d results with AcceptUnsolicited, want 1", len(got))
	}
}

func TestScanMergesOneReceiverSeenBySeveralLayers(t *testing.T) {
	c := &collector{
		cfg:   Config{Logger: slog.New(slog.DiscardHandler)},
		nonce: "n",
		seen:  map[string]int{},
	}

	a := Announce{V: Version, T: TypeAnnounce, DeviceID: "receiver-1", Port: 47891, Nonce: "n"}
	c.record(a, mustAddrPort("192.168.11.20:47890"), SourceMDNS)
	c.record(a, mustAddrPort("192.168.11.20:47890"), SourceBroadcast)
	c.record(a, mustAddrPort("192.168.11.20:47890"), SourceBroadcast)

	got := c.results()
	if len(got) != 1 {
		t.Fatalf("got %d results, want one merged receiver", len(got))
	}
	if len(got[0].Sources) != 2 {
		t.Fatalf("sources = %v, want mdns and broadcast recorded once each", got[0].Sources)
	}
}
