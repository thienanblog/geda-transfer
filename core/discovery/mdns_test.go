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
	"log/slog"
	"strings"
	"testing"
)

func testMDNS(t *testing.T, cfg ResponderConfig) *MDNSResponder {
	t.Helper()

	if cfg.Candidates == nil {
		cfg.Candidates = func() ([]string, error) { return []string{"192.168.11.20", "fd00::5"}, nil }
	}
	cfg.Logger = slog.New(slog.DiscardHandler)

	r, err := NewResponder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r.MDNS()
}

func TestMDNSResponseRoundTrips(t *testing.T) {
	m := testMDNS(t, ResponderConfig{
		DeviceID:     "8f14e45f-ea8f-4e9b-9c1a-3d2b6c7e0a11",
		Name:         "Studio Mac",
		Platform:     "darwin",
		TransferPort: 47891,
		SPKI:         "cGluLXZhbHVl",
	})

	raw, err := m.buildResponse()
	if err != nil {
		t.Fatal(err)
	}

	announces := parseMDNSResponse(raw)
	if len(announces) != 1 {
		t.Fatalf("parsed %d announces, want 1", len(announces))
	}

	a := announces[0]
	switch {
	case a.DeviceID != "8f14e45f-ea8f-4e9b-9c1a-3d2b6c7e0a11":
		t.Fatalf("device id = %q", a.DeviceID)
	case a.Name != "Studio Mac":
		t.Fatalf("name = %q", a.Name)
	case a.Platform != "darwin":
		t.Fatalf("platform = %q", a.Platform)
	case a.Port != 47891:
		t.Fatalf("port = %d", a.Port)
	case a.SPKI != "cGluLXZhbHVl":
		t.Fatalf("spki = %q", a.SPKI)
	}

	if len(a.Addrs) != 2 {
		t.Fatalf("addrs = %v, want both the IPv4 and IPv6 address", a.Addrs)
	}
}

func TestMDNSInstanceNameIsUniquePerDevice(t *testing.T) {
	// Two receivers with the same human name must not claim the same instance,
	// because nothing here implements RFC 6762 conflict resolution.
	a := testMDNS(t, ResponderConfig{DeviceID: "aaaaaaaa-0000-0000-0000-000000000001", Name: "Studio Mac", TransferPort: 47891})
	b := testMDNS(t, ResponderConfig{DeviceID: "bbbbbbbb-0000-0000-0000-000000000002", Name: "Studio Mac", TransferPort: 47891})

	if a.instanceName() == b.instanceName() {
		t.Fatalf("both receivers claim %s", a.instanceName())
	}
	for _, m := range []*MDNSResponder{a, b} {
		if !strings.HasSuffix(m.instanceName(), "."+ServiceName) {
			t.Fatalf("instance %q is not under %s", m.instanceName(), ServiceName)
		}
	}
}

func TestMDNSLabelSurvivesAwkwardNames(t *testing.T) {
	cases := map[string]string{
		"Studio Mac":  "Studio-Mac-deadbeef",
		"An's iPhone": "Ans-iPhone-deadbeef",
		"":            "geda-deadbeef",
		"🙂":           "geda-deadbeef",
		"...":         "geda-deadbeef",
	}

	for name, want := range cases {
		if got := mdnsLabel(name, "deadbeef-0000"); got != want {
			t.Errorf("mdnsLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestMDNSQueryIsAService(t *testing.T) {
	raw, err := mdnsQuery()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("empty query")
	}
	// A query is not a response, so a peer's parser must ignore it.
	if got := parseMDNSResponse(raw); len(got) != 0 {
		t.Fatalf("a query parsed as %d announces", len(got))
	}
}
