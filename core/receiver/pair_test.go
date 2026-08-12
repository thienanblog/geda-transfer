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

package receiver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/receiver"
)

type pairAnswer struct {
	Token          string   `json:"token"`
	DeviceID       string   `json:"device_id"`
	SPKI           string   `json:"spki"`
	Addrs          []string `json:"addrs"`
	NamingTemplate string   `json:"naming_template"`
	MaxConcurrency int      `json:"max_concurrency"`
}

func (h *harness) offer(t *testing.T) receiver.Offer {
	t.Helper()

	offer, err := h.srv.BeginPairing(0)
	if err != nil {
		t.Fatal(err)
	}
	return offer
}

func (h *harness) pair(t *testing.T, body map[string]any) (*http.Response, pairAnswer) {
	t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/v1/pair", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp := h.do(req)
	defer resp.Body.Close()

	var answer pairAnswer
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
			t.Fatal(err)
		}
	}
	return resp, answer
}

func pairBody(psk string) map[string]any {
	return map[string]any{
		"v":         pairing.Version,
		"psk":       psk,
		"device_id": "phone-7",
		"name":      "An's iPhone",
		"platform":  "ios",
	}
}

func TestPairingGrantsAWorkingToken(t *testing.T) {
	h := newHarness(t)
	offer := h.offer(t)

	resp, answer := h.pair(t, pairBody(offer.Payload.PSK))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pairing failed: %s", resp.Status)
	}
	if answer.Token == "" {
		t.Fatal("no token issued")
	}
	if answer.DeviceID != "receiver-1" {
		t.Fatalf("device id = %q, want the receiver's own id", answer.DeviceID)
	}
	if answer.NamingTemplate == "" || answer.MaxConcurrency == 0 {
		t.Fatalf("settings missing from the pairing response: %+v", answer)
	}

	// The point of the token is the requests it authorises.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/v1/have", strings.NewReader(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+answer.Token)

	probe := h.do(req)
	defer probe.Body.Close()
	if probe.StatusCode != http.StatusOK {
		t.Fatalf("the freshly issued token was rejected: %s", probe.Status)
	}
}

func TestPairingCodeIsSingleUse(t *testing.T) {
	h := newHarness(t)
	offer := h.offer(t)

	if resp, _ := h.pair(t, pairBody(offer.Payload.PSK)); resp.StatusCode != http.StatusOK {
		t.Fatalf("first pairing failed: %s", resp.Status)
	}

	// A replayed request -- from a photograph of the screen, or from anyone
	// who saw the code -- must not pair a second device.
	resp, _ := h.pair(t, pairBody(offer.Payload.PSK))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a replayed pairing code returned %s, want 401", resp.Status)
	}
}

func TestPairingRejectsAnUnknownCode(t *testing.T) {
	h := newHarness(t)
	h.offer(t)

	resp, _ := h.pair(t, pairBody("not-the-code"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a guessed code returned %s, want 401", resp.Status)
	}
}

func TestPairingRejectsIncompleteRequests(t *testing.T) {
	h := newHarness(t)

	cases := map[string]map[string]any{
		"wrong version": {"v": 99, "psk": h.offer(t).Payload.PSK, "device_id": "phone-7", "name": "An's iPhone"},
		"no device id":  {"v": pairing.Version, "psk": h.offer(t).Payload.PSK, "name": "An's iPhone"},
		"no name":       {"v": pairing.Version, "psk": h.offer(t).Payload.PSK, "device_id": "phone-7"},
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp, _ := h.pair(t, body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s returned %s, want 400", name, resp.Status)
			}
		})
	}
}

func TestCancelPairingInvalidatesTheCode(t *testing.T) {
	h := newHarness(t)
	offer := h.offer(t)

	h.srv.CancelPairing()

	resp, _ := h.pair(t, pairBody(offer.Payload.PSK))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a cancelled code returned %s, want 401", resp.Status)
	}
}

func TestOfferCarriesThePinAndTheCandidateSet(t *testing.T) {
	h := newHarness(t)
	offer := h.offer(t)

	decoded, err := pairing.Decode(offer.URI)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SPKI == "" {
		// Without the pin there is nothing to trust on first use.
		t.Fatal("the QR payload carries no pin")
	}
	if len(decoded.Addrs) == 0 {
		t.Fatal("the QR payload carries no addresses to connect to")
	}
	for _, addr := range decoded.Addrs {
		if !strings.Contains(addr, ":") {
			t.Fatalf("address %q has no port; a client cannot dial it", addr)
		}
	}
	if decoded.Exp == 0 {
		t.Fatal("the QR payload never expires")
	}
}

func TestRepairingKeepsTheDeviceAndItsHistory(t *testing.T) {
	h := newHarness(t)

	first := h.offer(t)
	_, firstAnswer := h.pair(t, pairBody(first.Payload.PSK))

	second := h.offer(t)
	_, secondAnswer := h.pair(t, pairBody(second.Payload.PSK))

	if firstAnswer.Token == secondAnswer.Token {
		t.Fatal("re-pairing reused the old token")
	}

	var rows int
	if err := h.db.SQL().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM devices WHERE id = 'phone-7'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d rows for the re-paired device, want one so its history survives", rows)
	}

	// The old token must stop working, or unpairing would be meaningless.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/v1/have", strings.NewReader(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+firstAnswer.Token)

	resp := h.do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the superseded token returned %s, want 401", resp.Status)
	}
}

func TestUnpairRevokesTheToken(t *testing.T) {
	h := newHarness(t)
	offer := h.offer(t)

	_, answer := h.pair(t, pairBody(offer.Payload.PSK))

	if err := h.srv.Unpair(t.Context(), "phone-7"); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/v1/have", strings.NewReader(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+answer.Token)

	resp := h.do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unpaired device still authenticates: %s", resp.Status)
	}

	if err := h.srv.Unpair(t.Context(), "phone-7"); err == nil {
		t.Fatal("unpairing an already unpaired device reported success")
	}
}
