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

package pairing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func samplePayload(t *testing.T) Payload {
	t.Helper()

	psk, err := NewPSK()
	if err != nil {
		t.Fatal(err)
	}
	return Payload{
		V:        Version,
		DeviceID: "8f14e45f-ea8f-4e9b-9c1a-3d2b6c7e0a11",
		Name:     "Studio Mac",
		SPKI:     "3q2+7wAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		Addrs:    []string{"192.168.11.20:47891", "10.13.13.5:47891"},
		PSK:      psk,
		Exp:      time.Now().Add(DefaultOfferTTL).Unix(),
	}
}

func TestPayloadRoundTrips(t *testing.T) {
	want := samplePayload(t)

	uri, err := want.URI()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, URIScheme) {
		t.Fatalf("uri = %q, want the %s scheme so a camera app can route it", uri, URIScheme)
	}

	got, err := Decode(uri)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case got.DeviceID != want.DeviceID:
		t.Fatalf("device id = %q", got.DeviceID)
	case got.SPKI != want.SPKI:
		t.Fatalf("spki = %q", got.SPKI)
	case got.PSK != want.PSK:
		t.Fatalf("psk = %q", got.PSK)
	case len(got.Addrs) != len(want.Addrs):
		t.Fatalf("addrs = %v", got.Addrs)
	}
}

func TestDecodeAcceptsABarePayload(t *testing.T) {
	p := samplePayload(t)

	encoded, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(encoded); err != nil {
		t.Fatalf("a payload without the URI scheme should still decode: %v", err)
	}
}

func TestDecodeRejectsUnusablePayloads(t *testing.T) {
	strip := func(mutate func(*Payload)) string {
		t.Helper()
		p := samplePayload(t)
		mutate(&p)
		encoded, err := p.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	cases := map[string]string{
		"no spki":       strip(func(p *Payload) { p.SPKI = "" }),
		"no psk":        strip(func(p *Payload) { p.PSK = "" }),
		"no device id":  strip(func(p *Payload) { p.DeviceID = "" }),
		"no addresses":  strip(func(p *Payload) { p.Addrs = nil }),
		"wrong version": strip(func(p *Payload) { p.V = 99 }),
		"not base64":    "!!!!",
	}

	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(encoded); !errors.Is(err, ErrBadPayload) {
				t.Fatalf("accepted a payload with %s: %v", name, err)
			}
		})
	}
}

func TestPayloadExpires(t *testing.T) {
	p := samplePayload(t)
	p.Exp = time.Now().Add(-time.Second).Unix()

	if !p.Expired(time.Now()) {
		// A photograph of yesterday's QR code must not be a working
		// credential.
		t.Fatal("an old payload is still considered valid")
	}
}

func TestOfferIsSingleUse(t *testing.T) {
	offers := NewOffers(nil)

	psk, _, err := offers.Issue(0)
	if err != nil {
		t.Fatal(err)
	}

	if !offers.Redeem(psk) {
		t.Fatal("a fresh offer was refused")
	}
	if offers.Redeem(psk) {
		t.Fatal("the same pairing code paired a second device")
	}
}

func TestOfferExpires(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	offers := NewOffers(clock)

	psk, _, err := offers.Issue(time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	if offers.Redeem(psk) {
		t.Fatal("an expired offer was accepted")
	}
}

func TestUnknownSecretIsRefused(t *testing.T) {
	offers := NewOffers(nil)
	if _, _, err := offers.Issue(0); err != nil {
		t.Fatal(err)
	}

	if offers.Redeem("not-the-secret") {
		t.Fatal("a guessed secret was accepted")
	}
	if offers.Redeem("") {
		t.Fatal("an empty secret was accepted")
	}
}

func TestRevokeDropsOutstandingOffers(t *testing.T) {
	offers := NewOffers(nil)

	psk, _, err := offers.Issue(0)
	if err != nil {
		t.Fatal(err)
	}

	// Closing the pairing screen must invalidate the code that was on it.
	offers.Revoke()
	if offers.Redeem(psk) {
		t.Fatal("a revoked offer still paired a device")
	}
	if offers.Len() != 0 {
		t.Fatalf("%d offers still live after revoke", offers.Len())
	}
}

func TestSecretsAreDistinct(t *testing.T) {
	seen := make(map[string]bool, 200)
	for i := 0; i < 100; i++ {
		psk, err := NewPSK()
		if err != nil {
			t.Fatal(err)
		}
		token, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{psk, token} {
			if seen[secret] {
				t.Fatal("a secret repeated")
			}
			if len(secret) < 40 {
				t.Fatalf("secret %q is too short to be unguessable", secret)
			}
			seen[secret] = true
		}
	}
}
