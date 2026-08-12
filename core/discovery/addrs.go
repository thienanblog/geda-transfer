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
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

// Interface is one usable local interface address.
type Interface struct {
	Name   string
	Addr   netip.Addr
	Prefix netip.Prefix

	// VPN marks a tunnel interface. Two things depend on it: these addresses
	// are the ones that make a paired peer reachable from another subnet, and
	// iOS does not treat a VPN address as "local network", so connecting to
	// one raises no permission prompt.
	VPN bool
}

// vpnPrefixes are the interface-name prefixes used by tunnels on the platforms
// this project ships on: utun (macOS/iOS), wg (WireGuard everywhere), tun/tap
// (OpenVPN and friends), ppp, ipsec.
var vpnPrefixes = []string{"utun", "wg", "tun", "tap", "ppp", "ipsec"}

// IsVPNInterface reports whether a name looks like a tunnel.
func IsVPNInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range vpnPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// LocalInterfaces returns every address worth advertising or probing from.
//
// Tunnel addresses are included deliberately. They are the whole reason a
// phone on a remote subnet can reach a desktop over WireGuard: the desktop
// advertises its `utun`/`wg` address as part of the candidate set, and the
// phone races a connection to it (AGENTS.md §3.4). Filtering interfaces down
// to "the real LAN one" would break exactly the case this design exists for.
//
// Loopback, link-local and multicast addresses are excluded: no peer can reach
// this host through them.
func LocalInterfaces() ([]Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("discovery: list interfaces: %w", err)
	}

	var out []Interface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			// One unreadable interface must not hide the rest.
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			prefix, ok := toPrefix(ipnet)
			if !ok || !usableAddr(prefix.Addr()) {
				continue
			}
			out = append(out, Interface{
				Name:   iface.Name,
				Addr:   prefix.Addr(),
				Prefix: prefix.Masked(),
				VPN:    IsVPNInterface(iface.Name),
			})
		}
	}

	sortInterfaces(out)
	return out, nil
}

// Candidates renders the local addresses as the candidate set of an announce.
func Candidates() ([]string, error) {
	ifaces, err := LocalInterfaces()
	if err != nil {
		return nil, err
	}

	seen := make(map[netip.Addr]bool, len(ifaces))
	out := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		if seen[iface.Addr] {
			continue
		}
		seen[iface.Addr] = true
		out = append(out, iface.Addr.String())
	}
	return out, nil
}

func toPrefix(ipnet *net.IPNet) (netip.Prefix, bool) {
	addr, ok := netip.AddrFromSlice(ipnet.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()

	ones, _ := ipnet.Mask.Size()
	if ones == 0 && addr.Is4() {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, ones), true
}

func usableAddr(addr netip.Addr) bool {
	switch {
	case !addr.IsValid(),
		addr.IsLoopback(),
		addr.IsUnspecified(),
		addr.IsMulticast(),
		addr.IsLinkLocalUnicast(),
		addr.IsLinkLocalMulticast():
		return false
	}
	return true
}

// sortInterfaces puts the addresses most likely to work first: IPv4 before
// IPv6, LAN before tunnel. Candidate racing starts connections in this order
// with a stagger, so the cheapest route is tried first.
func sortInterfaces(in []Interface) {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].VPN != in[j].VPN {
			return !in[i].VPN
		}
		if in[i].Addr.Is4() != in[j].Addr.Is4() {
			return in[i].Addr.Is4()
		}
		return in[i].Addr.String() < in[j].Addr.String()
	})
}

// BroadcastAddrs returns the directed broadcast address of every IPv4
// interface, plus the all-hosts address.
//
// Directed broadcast is needed because some stacks do not deliver
// 255.255.255.255 out of every interface, and the all-hosts address is needed
// because some interfaces report a mask that makes the directed address
// useless. Sending to both costs two datagrams.
func BroadcastAddrs(ifaces []Interface) []netip.Addr {
	out := []netip.Addr{netip.AddrFrom4([4]byte{255, 255, 255, 255})}
	seen := map[netip.Addr]bool{out[0]: true}

	for _, iface := range ifaces {
		if !iface.Addr.Is4() || iface.VPN {
			// A tunnel is point-to-point: it has no broadcast domain, so a
			// broadcast address derived from its prefix goes nowhere.
			continue
		}
		bcast, ok := broadcastOf(iface.Prefix)
		if !ok || seen[bcast] {
			continue
		}
		seen[bcast] = true
		out = append(out, bcast)
	}
	return out
}

func broadcastOf(prefix netip.Prefix) (netip.Addr, bool) {
	if !prefix.Addr().Is4() || prefix.Bits() >= 31 {
		return netip.Addr{}, false
	}

	base := prefix.Masked().Addr().As4()
	hostBits := 32 - prefix.Bits()
	for i := 0; i < hostBits; i++ {
		base[3-i/8] |= 1 << (i % 8)
	}
	return netip.AddrFrom4(base), true
}

// maxSweepHosts caps one CIDR. A /24 is 254 probes and finishes in about a
// second; a careless /8 would be sixteen million and is refused rather than
// silently truncated.
const maxSweepHosts = 4096

// SweepHosts expands a prefix into the addresses a unicast sweep probes.
//
// Network and broadcast addresses are skipped for IPv4 prefixes shorter than
// /31, since no host answers on them.
func SweepHosts(prefix netip.Prefix) ([]netip.Addr, error) {
	prefix = prefix.Masked()
	if !prefix.IsValid() {
		return nil, fmt.Errorf("discovery: invalid prefix %q", prefix)
	}

	hostBits := prefix.Addr().BitLen() - prefix.Bits()
	if hostBits > 20 || 1<<uint(hostBits) > maxSweepHosts+2 {
		return nil, fmt.Errorf("discovery: %s covers more than %d hosts; use a narrower range", prefix, maxSweepHosts)
	}

	skipEdges := prefix.Addr().Is4() && prefix.Bits() < 31

	var out []netip.Addr
	for addr := prefix.Addr(); prefix.Contains(addr); addr = addr.Next() {
		if skipEdges && (addr == prefix.Addr() || isBroadcast(prefix, addr)) {
			continue
		}
		out = append(out, addr)
	}
	return out, nil
}

func isBroadcast(prefix netip.Prefix, addr netip.Addr) bool {
	bcast, ok := broadcastOf(prefix)
	return ok && addr == bcast
}
