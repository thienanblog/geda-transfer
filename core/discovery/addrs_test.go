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
	"testing"
)

func mustAddrPort(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func TestIsVPNInterface(t *testing.T) {
	vpn := []string{"utun0", "utun9", "wg0", "wg-home", "tun0", "ppp0", "ipsec1", "UTUN3"}
	lan := []string{"en0", "eth0", "lo0", "bridge100", "awdl0"}

	for _, name := range vpn {
		if !IsVPNInterface(name) {
			t.Errorf("%s should be recognised as a tunnel: its address is what makes cross-subnet reconnection work", name)
		}
	}
	for _, name := range lan {
		if IsVPNInterface(name) {
			t.Errorf("%s should not be treated as a tunnel", name)
		}
	}
}

func TestSweepHostsSkipsNetworkAndBroadcast(t *testing.T) {
	hosts, err := SweepHosts(netip.MustParsePrefix("192.168.11.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 254 {
		t.Fatalf("swept %d hosts, want 254", len(hosts))
	}
	if hosts[0].String() != "192.168.11.1" {
		t.Fatalf("first host is %s, want 192.168.11.1", hosts[0])
	}
	if last := hosts[len(hosts)-1]; last.String() != "192.168.11.254" {
		t.Fatalf("last host is %s, want 192.168.11.254", last)
	}
}

func TestSweepHostsAcceptsAHostAddressAsAPrefix(t *testing.T) {
	hosts, err := SweepHosts(netip.MustParsePrefix("192.168.11.20/32"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].String() != "192.168.11.20" {
		t.Fatalf("got %v, want [192.168.11.20]", hosts)
	}
}

func TestSweepHostsRefusesAHugeRange(t *testing.T) {
	if _, err := SweepHosts(netip.MustParsePrefix("10.0.0.0/8")); err == nil {
		t.Fatal("a /8 sweep should be refused rather than silently truncated")
	}
}

func TestBroadcastAddrsExcludeTunnels(t *testing.T) {
	ifaces := []Interface{
		{Name: "en0", Addr: netip.MustParseAddr("192.168.11.20"), Prefix: netip.MustParsePrefix("192.168.11.0/24")},
		{Name: "utun3", Addr: netip.MustParseAddr("10.13.13.5"), Prefix: netip.MustParsePrefix("10.13.13.0/24"), VPN: true},
	}

	got := BroadcastAddrs(ifaces)

	want := map[string]bool{"255.255.255.255": true, "192.168.11.255": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, addr := range got {
		if !want[addr.String()] {
			// A tunnel has no broadcast domain, so a broadcast address
			// derived from its prefix is a packet sent nowhere.
			t.Fatalf("unexpected broadcast target %s", addr)
		}
	}
}

func TestLocalInterfacesExcludeLoopbackAndLinkLocal(t *testing.T) {
	ifaces, err := LocalInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range ifaces {
		if iface.Addr.IsLoopback() || iface.Addr.IsLinkLocalUnicast() {
			t.Fatalf("%s (%s) is not reachable by a peer and should not be advertised", iface.Name, iface.Addr)
		}
	}
}

func TestCandidatesAreDeduplicated(t *testing.T) {
	addrs, err := Candidates()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		if seen[a] {
			t.Fatalf("address %s advertised twice", a)
		}
		seen[a] = true
	}
}
