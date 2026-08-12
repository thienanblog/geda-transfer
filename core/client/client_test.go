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

package client_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/client"
	"github.com/geda/geda-transfer/core/identity"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"
)

type testReceiver struct {
	srv  *receiver.Server
	id   *identity.Identity
	addr string
}

// newReceiver starts a real TLS receiver on loopback. Nothing here is faked:
// pinning is only meaningful against an actual handshake.
func newReceiver(t *testing.T) *testReceiver {
	t.Helper()

	dir := t.TempDir()

	db, err := store.Open(t.Context(), filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	files, err := storage.New(db, filepath.Join(dir, "Photos"))
	if err != nil {
		t.Fatal(err)
	}

	id, err := identity.Load(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}

	srv, err := receiver.New(receiver.Config{
		DeviceID: "receiver-1",
		Name:     "Studio Mac",
		DB:       db,
		Files:    files,
		Identity: id,
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if err := srv.Serve(ctx, ln); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()

	return &testReceiver{srv: srv, id: id, addr: ln.Addr().String()}
}

// offer builds a pairing payload pointing at the listener's real address.
func (r *testReceiver) offer(t *testing.T) pairing.Payload {
	t.Helper()

	offer, err := r.srv.BeginPairing(0)
	if err != nil {
		t.Fatal(err)
	}
	payload := offer.Payload
	payload.Addrs = []string{r.addr}
	return payload
}

var self = client.Device{ID: "phone-7", Name: "An's iPhone", Platform: "ios"}

func TestPairingOverPinnedTLS(t *testing.T) {
	rec := newReceiver(t)

	c, result, err := client.PairWith(t.Context(), rec.offer(t), self, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Token == "" {
		t.Fatal("no token")
	}
	if result.SPKI != rec.id.Pin {
		t.Fatalf("spki = %q, want the receiver's pin", result.SPKI)
	}

	// The token is only real if it opens an authenticated endpoint.
	if _, err := c.Have(t.Context(), []client.HaveItem{{ID: "a", Size: 1, HeadHash: "deadbeef"}}); err != nil {
		t.Fatalf("the token issued by pairing does not work: %v", err)
	}

	info, err := c.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if info.DeviceID != "receiver-1" {
		t.Fatalf("info device id = %q", info.DeviceID)
	}
}

func TestPinMismatchIsFatal(t *testing.T) {
	rec := newReceiver(t)

	// A different key entirely: what a client sees if the machine it paired
	// with has been replaced, or is being impersonated.
	other, err := identity.Load(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}

	c, err := client.New(client.Config{Pin: other.Pin, Addrs: []string{rec.addr}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Info(t.Context()); !errors.Is(err, client.ErrPinMismatch) {
		t.Fatalf("a certificate that does not match the pin was accepted: %v", err)
	}
}

func TestPairingRefusesAnExpiredCode(t *testing.T) {
	rec := newReceiver(t)

	payload := rec.offer(t)
	payload.Exp = time.Now().Add(-time.Minute).Unix()

	if _, _, err := client.PairWith(t.Context(), payload, self, client.Config{}); !errors.Is(err, pairing.ErrBadPayload) {
		t.Fatalf("an expired pairing code was used: %v", err)
	}
}

func TestPairingDetectsADifferentReceiver(t *testing.T) {
	rec := newReceiver(t)

	payload := rec.offer(t)
	payload.DeviceID = "some-other-receiver"

	if _, _, err := client.PairWith(t.Context(), payload, self, client.Config{}); !errors.Is(err, client.ErrDeviceMismatch) {
		t.Fatalf("paired with a receiver that is not the one in the code: %v", err)
	}
}

// TestRacingSkipsUnreachableCandidates is the reconnect path: a paired client
// holds every address the receiver ever had -- LAN, VPN, IPv6 -- and cannot
// know which of them works from where it is now.
func TestRacingSkipsUnreachableCandidates(t *testing.T) {
	rec := newReceiver(t)

	// 192.0.2.0/24 is TEST-NET-1: reserved, routed nowhere, so the connection
	// hangs exactly like a LAN address does from another subnet.
	addrs := []string{"192.0.2.10:47891", "192.0.2.11:47891", rec.addr}

	c, err := client.New(client.Config{
		Pin:         rec.id.Pin,
		Addrs:       addrs,
		DialTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := c.Info(t.Context()); err != nil {
		t.Fatalf("racing did not fall through to the reachable address: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("connected after %s; the losers are not being cancelled", elapsed)
	}

	// The address that worked is tried first next time, so a reconnect does
	// not pay the stagger again.
	if got := c.Addrs(); got[0] != rec.addr {
		t.Fatalf("candidate order = %v, want the working address first", got)
	}
}

func TestNoCandidateAnswers(t *testing.T) {
	c, err := client.New(client.Config{
		Pin:         "some-pin",
		Addrs:       []string{"192.0.2.10:47891"},
		DialTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Info(t.Context()); err == nil {
		t.Fatal("connecting to nothing reported success")
	}
}

func TestClientRequiresPinAndAddresses(t *testing.T) {
	if _, err := client.New(client.Config{Addrs: []string{"127.0.0.1:1"}}); err == nil {
		t.Fatal("a client without a pin was allowed: that is an unauthenticated connection")
	}
	if _, err := client.New(client.Config{Pin: "p"}); err == nil {
		t.Fatal("a client with no addresses was allowed")
	}
}
