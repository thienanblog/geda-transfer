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
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProbeIsPaddedPastTheAnnounce(t *testing.T) {
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}

	raw, err := MarshalProbe(nonce)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < MinProbeSize {
		t.Fatalf("probe is %d bytes, want at least %d", len(raw), MinProbeSize)
	}

	// The whole point of the padding is that answering costs the responder
	// fewer bytes than asking cost the sender, so a spoofed source cannot
	// amplify. Compare against a realistically large announce.
	announce, err := json.Marshal(Announce{
		V: Version, T: TypeAnnounce, Nonce: nonce,
		DeviceID: "8f14e45f-ea8f-4e9b-9c1a-3d2b6c7e0a11",
		Name:     "Studio Mac in the back room",
		Platform: "darwin", Port: DefaultTransferPort,
		SPKI:  strings.Repeat("A", 44),
		Addrs: []string{"192.168.11.20", "10.13.13.5", "fd00::5", "192.168.12.44"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(announce) >= len(raw) {
		t.Fatalf("announce (%d bytes) is not smaller than the probe (%d bytes): amplification is possible",
			len(announce), len(raw))
	}
}

func TestParseProbeRejectsUnpaddedProbe(t *testing.T) {
	raw, err := json.Marshal(Probe{V: Version, T: TypeProbe, Nonce: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProbe(raw); !errors.Is(err, ErrNotProbe) {
		t.Fatalf("short probe accepted: %v", err)
	}
}

func TestParseProbeRejectsMalformed(t *testing.T) {
	pad := strings.Repeat("x", MinProbeSize)

	cases := map[string]string{
		"wrong type":    `{"v":1,"t":"announce","nonce":"n","pad":"` + pad + `"}`,
		"wrong version": `{"v":99,"t":"probe","nonce":"n","pad":"` + pad + `"}`,
		"no nonce":      `{"v":1,"t":"probe","pad":"` + pad + `"}`,
		"not json":      strings.Repeat("!", MinProbeSize+1),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseProbe([]byte(raw)); !errors.Is(err, ErrNotProbe) {
				t.Fatalf("accepted %s: %v", name, err)
			}
		})
	}
}

func TestParseProbeAcceptsAValidProbe(t *testing.T) {
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalProbe(nonce)
	if err != nil {
		t.Fatal(err)
	}

	probe, err := ParseProbe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Nonce != nonce {
		t.Fatalf("nonce = %q, want %q", probe.Nonce, nonce)
	}
}

func TestNoncesAreUnique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		n, err := NewNonce()
		if err != nil {
			t.Fatal(err)
		}
		if seen[n] {
			t.Fatal("nonce repeated")
		}
		seen[n] = true
	}
}

func TestParseAnnounceRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"wrong type":    `{"v":1,"t":"probe","device_id":"a","port":1}`,
		"wrong version": `{"v":2,"t":"announce","device_id":"a","port":1}`,
		"no device id":  `{"v":1,"t":"announce","port":1}`,
		"no port":       `{"v":1,"t":"announce","device_id":"a"}`,
		"not json":      `nonsense`,
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAnnounce([]byte(raw)); !errors.Is(err, ErrNotAnnounce) {
				t.Fatalf("accepted %s: %v", name, err)
			}
		})
	}
}

func TestTransferAddrsPutsTheWorkingAddressFirst(t *testing.T) {
	r := Result{
		Announce: Announce{
			Port:  47891,
			Addrs: []string{"192.168.11.20", "10.13.13.5"},
		},
	}
	r.From = mustAddrPort("10.13.13.5:47890")

	got := r.TransferAddrs()
	want := []string{"10.13.13.5:47891", "192.168.11.20:47891"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
