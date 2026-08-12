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
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Source records which layer produced a result.
type Source string

const (
	SourceMDNS      Source = "mdns"      // L1
	SourceBroadcast Source = "broadcast" // L2
	SourceSweep     Source = "sweep"     // L3
	SourceCandidate Source = "candidate" // L4
	SourceManual    Source = "manual"    // L5
)

// Result is one receiver seen during a scan.
type Result struct {
	Announce

	// Sources are the layers that reported this receiver, in the order they
	// did. Cross-subnet peers arrive by sweep or candidate; mDNS and broadcast
	// cannot cross a router.
	Sources []Source

	// From is where the answer came from, which is not necessarily an address
	// in Addrs -- NAT and tunnels both rewrite it.
	From netip.AddrPort

	At time.Time
}

// TransferAddrs returns the candidate set as host:port strings, with the
// answering address first: it demonstrably works right now, so it is the one
// worth trying first.
func (r Result) TransferAddrs() []string {
	port := r.Port
	if port == 0 {
		port = DefaultTransferPort
	}

	out := make([]string, 0, len(r.Addrs)+1)
	seen := make(map[string]bool, len(r.Addrs)+1)

	add := func(host string) {
		hp := net.JoinHostPort(host, strconv.Itoa(port))
		if !seen[hp] {
			seen[hp] = true
			out = append(out, hp)
		}
	}

	if r.From.IsValid() {
		add(r.From.Addr().String())
	}
	for _, a := range r.Addrs {
		add(a)
	}
	return out
}

// Config controls one scan.
type Config struct {
	// Port is the discovery port. Defaults to DefaultPort.
	Port int

	// Subnets are the CIDRs swept by unicast (L3). This is the layer that
	// crosses subnet boundaries, where mDNS and broadcast cannot.
	Subnets []netip.Prefix

	// Candidates are addresses of already-paired peers (L4), as "host" or
	// "host:port". They are probed directly wherever they are, which is what
	// finds a peer over WireGuard.
	Candidates []string

	// Manual are user-entered addresses (L5), same forms as Candidates.
	Manual []string

	// Timeout bounds the scan. Defaults to 3s, the figure the cross-subnet
	// gate in docs/PLAN.md is measured against.
	Timeout time.Duration

	// Rounds is how many times the probe set is sent. Two is the default: on
	// a cold ARP table the first datagram to an unknown host is dropped while
	// the address is resolved, so a single round systematically misses hosts
	// that are in fact present.
	Rounds int

	// RoundInterval spaces the rounds. Defaults to 400ms.
	RoundInterval time.Duration

	// DisableMDNS turns off L1.
	DisableMDNS bool

	// DisableBroadcast turns off L2. Useful on segments where broadcast is
	// filtered and the packets are pure noise, and in tests that need to prove
	// a receiver was found by unicast alone -- which is the property that
	// crosses a subnet boundary.
	DisableBroadcast bool

	// AcceptUnsolicited accepts announces that quote no nonce -- the periodic
	// broadcasts. Off by default: during a scan, requiring the echo is what
	// makes off-path injection useless.
	AcceptUnsolicited bool

	// OnResult is called as each new receiver appears, for live UI. Optional.
	OnResult func(Result)

	Logger *slog.Logger
}

func (c *Config) applyDefaults() {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Timeout == 0 {
		c.Timeout = 3 * time.Second
	}
	if c.Rounds == 0 {
		c.Rounds = 2
	}
	if c.RoundInterval == 0 {
		c.RoundInterval = 400 * time.Millisecond
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Discover runs every layer in parallel and returns the merged result set.
//
// It always runs to the timeout rather than stopping at the first answer: a
// scan is a list for the user to choose from, and the cross-subnet peer is
// usually not the first to reply.
func Discover(ctx context.Context, cfg Config) ([]Result, error) {
	cfg.applyDefaults()

	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	nonce, err := NewNonce()
	if err != nil {
		return nil, err
	}
	probe, err := MarshalProbe(nonce)
	if err != nil {
		return nil, err
	}

	targets, err := buildTargets(cfg)
	if err != nil {
		return nil, err
	}

	c := &collector{cfg: cfg, nonce: nonce, targets: targets, seen: map[string]int{}}

	conn4, err := listenSender(ctx, "udp4")
	if err != nil {
		return nil, err
	}
	defer conn4.Close()

	conn6, err6 := listenSender(ctx, "udp6")
	if err6 == nil {
		defer conn6.Close()
	}

	var wg sync.WaitGroup
	read := func(pc net.PacketConn) {
		defer wg.Done()
		c.readLoop(ctx, pc)
	}

	wg.Add(1)
	go read(conn4)
	if err6 == nil {
		wg.Add(1)
		go read(conn6)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.sendRounds(ctx, conn4, conn6, probe)
	}()

	if !cfg.DisableMDNS {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.browseMDNS(ctx)
		}()
	}

	<-ctx.Done()
	conn4.Close()
	if err6 == nil {
		conn6.Close()
	}
	wg.Wait()

	return c.results(), nil
}

// First runs a scan and returns as soon as a receiver matching want appears.
//
// This is the reconnect path rather than the browse path: a client that
// already knows which peer it wants has no reason to wait out the full scan
// window once that peer has answered.
func First(ctx context.Context, cfg Config, want func(Result) bool) (Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	hit := make(chan Result, 1)
	onResult := cfg.OnResult
	cfg.OnResult = func(r Result) {
		if onResult != nil {
			onResult(r)
		}
		if want == nil || want(r) {
			select {
			case hit <- r:
				cancel()
			default:
			}
		}
	}

	if _, err := Discover(ctx, cfg); err != nil {
		return Result{}, err
	}

	select {
	case r := <-hit:
		return r, nil
	default:
		return Result{}, ErrNotFound
	}
}

// ErrNotFound reports a scan that ended without the peer answering.
var ErrNotFound = errors.New("discovery: no matching receiver answered")

// target is one address a probe is sent to, and the layer it belongs to.
type target struct {
	addr   netip.Addr
	source Source
}

func buildTargets(cfg Config) ([]target, error) {
	var out []target
	seen := make(map[netip.Addr]bool)

	add := func(addr netip.Addr, src Source) {
		addr = addr.Unmap()
		if !addr.IsValid() || seen[addr] {
			return
		}
		seen[addr] = true
		out = append(out, target{addr: addr, source: src})
	}

	// L4 and L5 first: a paired peer is what the user is usually waiting for,
	// and these are a handful of packets rather than hundreds.
	for _, raw := range cfg.Candidates {
		if addr, ok := parseHost(raw); ok {
			add(addr, SourceCandidate)
		}
	}
	for _, raw := range cfg.Manual {
		if addr, ok := parseHost(raw); ok {
			add(addr, SourceManual)
		}
	}

	// L2.
	if !cfg.DisableBroadcast {
		ifaces, err := LocalInterfaces()
		if err != nil {
			cfg.Logger.Debug("discovery: could not list interfaces", "error", err)
		}
		for _, b := range BroadcastAddrs(ifaces) {
			add(b, SourceBroadcast)
		}
		add(MulticastGroupV4, SourceBroadcast)
		add(MulticastGroupV6, SourceBroadcast)
	}

	// L3.
	for _, prefix := range cfg.Subnets {
		hosts, err := SweepHosts(prefix)
		if err != nil {
			return nil, err
		}
		for _, h := range hosts {
			add(h, SourceSweep)
		}
	}

	return out, nil
}

// parseHost accepts "host" or "host:port" and keeps only the address. The port
// in a candidate entry is the transfer port; discovery has its own.
func parseHost(raw string) (netip.Addr, bool) {
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(raw); err == nil {
		return ap.Addr().Unmap(), true
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			return addr.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

type collector struct {
	cfg     Config
	nonce   string
	targets []target

	mu   sync.Mutex
	seen map[string]int // device id -> index into found
	// found is kept as a slice so that the order results were first observed
	// -- which is roughly "fastest to answer" -- survives to the caller.
	found []Result
}

// sendBurst is how many datagrams go out before yielding briefly. A /24 sweep
// fired as fast as the loop can write overruns the socket send buffer and the
// neighbour-resolution queue on some stacks, and the excess is dropped
// silently. Pausing every burst costs a few milliseconds over a whole sweep.
const sendBurst = 32

// burstPause is the yield between bursts.
const burstPause = 2 * time.Millisecond

func (c *collector) sendRounds(ctx context.Context, conn4, conn6 net.PacketConn, probe []byte) {
	for round := 0; round < c.cfg.Rounds; round++ {
		if round > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(c.cfg.RoundInterval):
			}
		}
		c.sendAll(ctx, conn4, conn6, probe)
	}
}

func (c *collector) sendAll(ctx context.Context, conn4, conn6 net.PacketConn, probe []byte) {
	for i, t := range c.targets {
		if ctx.Err() != nil {
			return
		}

		pc := conn4
		if t.addr.Is6() {
			if conn6 == nil {
				continue
			}
			pc = conn6
		}
		dst := &net.UDPAddr{IP: t.addr.AsSlice(), Port: c.cfg.Port}
		if _, err := pc.WriteTo(probe, dst); err != nil {
			// Hosts that are unreachable, and interfaces that refuse
			// broadcast, are entirely normal during a sweep.
			c.cfg.Logger.Debug("discovery: probe failed", "target", t.addr.String(), "error", err)
		}

		if (i+1)%sendBurst == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(burstPause):
			}
		}
	}
}

func (c *collector) readLoop(ctx context.Context, pc net.PacketConn) {
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				c.cfg.Logger.Debug("discovery: read failed", "error", err)
			}
			return
		}

		announce, err := ParseAnnounce(buf[:n])
		if err != nil {
			continue
		}
		src, ok := addrPortOf(from)
		if !ok {
			continue
		}
		c.add(announce, src)
	}
}

func (c *collector) add(a Announce, from netip.AddrPort) {
	switch {
	case a.Nonce == c.nonce:
	case a.Nonce == "" && c.cfg.AcceptUnsolicited:
	default:
		// Either a stale reply or an announce this scan did not ask for.
		// Discarding it is what denies an off-path spoofer a free insertion.
		return
	}
	c.record(a, from, c.sourceFor(from.Addr()))
}

// record merges one sighting. mDNS enters here directly: it is a different
// protocol with no nonce of ours to echo, and its own message format is what
// bounds what an answer can claim.
func (c *collector) record(a Announce, from netip.AddrPort, source Source) {
	c.mu.Lock()
	if idx, ok := c.seen[a.DeviceID]; ok {
		c.found[idx].Sources = appendSource(c.found[idx].Sources, source)
		c.mu.Unlock()
		return
	}

	result := Result{Announce: a, Sources: []Source{source}, From: from, At: time.Now()}
	c.seen[a.DeviceID] = len(c.found)
	c.found = append(c.found, result)
	c.mu.Unlock()

	if c.cfg.OnResult != nil {
		c.cfg.OnResult(result)
	}
}

// sourceFor attributes an answer to the layer that provoked it. A reply to the
// broadcast probe arrives from the peer's own unicast address, so the target
// list is consulted rather than the destination the probe went to.
func (c *collector) sourceFor(addr netip.Addr) Source {
	for _, t := range c.targets {
		if t.addr == addr {
			return t.source
		}
	}
	return SourceBroadcast
}

func appendSource(list []Source, s Source) []Source {
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

func (c *collector) results() []Result {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Result, len(c.found))
	copy(out, c.found)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}
