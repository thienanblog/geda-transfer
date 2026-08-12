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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// ResponderConfig describes the receiver an announce advertises.
type ResponderConfig struct {
	DeviceID string
	Name     string
	Platform string

	// TransferPort is the TCP port clients connect to. Defaults to
	// DefaultTransferPort.
	TransferPort int

	// Port is the UDP port probes arrive on. Defaults to DefaultPort.
	Port int

	// SPKI is base64(SHA-256(SubjectPublicKeyInfo)) of the TLS identity.
	SPKI string

	// Paired reports whether any device is paired. Optional; a UI hint.
	Paired func() bool

	// Candidates supplies the advertised address set. Defaults to the local
	// interface addresses, tunnels included.
	Candidates func() ([]string, error)

	Logger *slog.Logger
}

// Responder answers discovery probes and announces itself periodically.
type Responder struct {
	cfg   ResponderConfig
	log   *slog.Logger
	limit *limiter

	mu       sync.Mutex
	cached   []string
	cachedAt time.Time
}

// candidateTTL keeps a broadcast storm from turning every probe into a syscall
// sweep of every interface. Addresses change on the order of seconds at worst
// -- a DHCP renewal or a VPN coming up -- so a few seconds of staleness costs
// nothing and one more announce round corrects it.
const candidateTTL = 5 * time.Second

// NewResponder builds a responder.
func NewResponder(cfg ResponderConfig) (*Responder, error) {
	if cfg.DeviceID == "" {
		return nil, errors.New("discovery: DeviceID is required")
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.TransferPort == 0 {
		cfg.TransferPort = DefaultTransferPort
	}
	if cfg.Candidates == nil {
		cfg.Candidates = Candidates
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Responder{
		cfg:   cfg,
		log:   cfg.Logger,
		limit: newLimiter(AnnouncesPerSecond, time.Now),
	}, nil
}

// Serve binds the discovery sockets and answers probes until ctx is done.
//
// IPv6 is best effort: a host with IPv6 disabled is entirely normal and must
// not stop IPv4 discovery from working.
func (r *Responder) Serve(ctx context.Context) error {
	conn4, err := listenUDP(ctx, "udp4", r.cfg.Port, false)
	if err != nil {
		return fmt.Errorf("discovery: listen udp4: %w", err)
	}
	defer conn4.Close()
	r.joinIPv4(conn4)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.serveConn(ctx, conn4)
	}()

	if conn6, err := listenUDP(ctx, "udp6", r.cfg.Port, false); err == nil {
		defer conn6.Close()
		r.joinIPv6(conn6)

		wg.Add(1)
		go func() {
			defer wg.Done()
			r.serveConn(ctx, conn6)
		}()
	} else {
		r.log.Debug("discovery: IPv6 unavailable", "error", err)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		r.announceLoop(ctx, conn4)
	}()

	<-ctx.Done()
	conn4.Close()
	wg.Wait()
	return nil
}

// ServeConn answers probes on one packet connection. Serve uses it; tests and
// embedders can supply their own transport.
func (r *Responder) ServeConn(ctx context.Context, pc net.PacketConn) {
	r.serveConn(ctx, pc)
}

func (r *Responder) serveConn(ctx context.Context, pc net.PacketConn) {
	go func() {
		<-ctx.Done()
		// Unblocks the read below; the loop then sees the error and returns.
		pc.Close()
	}()

	evict := time.NewTicker(idleEviction)
	defer evict.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-evict.C:
				r.limit.evictIdle()
			}
		}
	}()

	buf := make([]byte, maxDatagram)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				r.log.Warn("discovery: read failed", "error", err)
			}
			return
		}
		r.handle(pc, buf[:n], from)
	}
}

func (r *Responder) handle(pc net.PacketConn, raw []byte, from net.Addr) {
	probe, err := ParseProbe(raw)
	if err != nil {
		// Silence is the answer to anything malformed or under-padded. An
		// error reply would hand back the amplification the padding rule
		// exists to deny.
		return
	}

	src, ok := addrPortOf(from)
	if !ok {
		return
	}
	if !r.limit.allow(src.Addr()) {
		return
	}

	msg, err := r.announceBytes(probe.Nonce)
	if err != nil {
		r.log.Warn("discovery: could not build announce", "error", err)
		return
	}
	if _, err := pc.WriteTo(msg, from); err != nil {
		r.log.Debug("discovery: could not answer probe", "peer", src.String(), "error", err)
	}
}

// Announce returns what this responder would send, for tests and for the
// pairing QR payload.
func (r *Responder) Announce(nonce string) (Announce, error) {
	addrs, err := r.candidates()
	if err != nil {
		return Announce{}, err
	}

	paired := false
	if r.cfg.Paired != nil {
		paired = r.cfg.Paired()
	}

	return Announce{
		V:        Version,
		T:        TypeAnnounce,
		Nonce:    nonce,
		DeviceID: r.cfg.DeviceID,
		Name:     r.cfg.Name,
		Platform: r.cfg.Platform,
		Port:     r.cfg.TransferPort,
		SPKI:     r.cfg.SPKI,
		Addrs:    addrs,
		Paired:   paired,
	}, nil
}

func (r *Responder) announceBytes(nonce string) ([]byte, error) {
	a, err := r.Announce(nonce)
	if err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

func (r *Responder) candidates() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if time.Since(r.cachedAt) < candidateTTL && r.cached != nil {
		return r.cached, nil
	}

	addrs, err := r.cfg.Candidates()
	if err != nil {
		if r.cached != nil {
			// A transient failure to enumerate interfaces should not make a
			// reachable receiver look unreachable.
			return r.cached, nil
		}
		return nil, err
	}

	r.cached, r.cachedAt = addrs, time.Now()
	return addrs, nil
}

// announceLoop sends unsolicited announces so that a client which is merely
// listening -- not sweeping -- still learns about this receiver.
func (r *Responder) announceLoop(ctx context.Context, pc net.PacketConn) {
	send := func() {
		msg, err := r.announceBytes("")
		if err != nil {
			r.log.Debug("discovery: could not build announce", "error", err)
			return
		}

		ifaces, err := LocalInterfaces()
		if err != nil {
			r.log.Debug("discovery: could not list interfaces", "error", err)
			return
		}

		targets := BroadcastAddrs(ifaces)
		targets = append(targets, MulticastGroupV4)
		for _, target := range targets {
			dst := &net.UDPAddr{IP: target.AsSlice(), Port: r.cfg.Port}
			if _, err := pc.WriteTo(msg, dst); err != nil {
				r.log.Debug("discovery: announce failed", "target", target.String(), "error", err)
			}
		}
	}
	send()

	ticker := time.NewTicker(AnnounceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func (r *Responder) joinIPv4(pc net.PacketConn) {
	p := ipv4.NewPacketConn(pc)
	group := &net.UDPAddr{IP: MulticastGroupV4.AsSlice()}

	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	joined := 0
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagMulticast == 0 || ifaces[i].Flags&net.FlagUp == 0 {
			continue
		}
		if err := p.JoinGroup(&ifaces[i], group); err == nil {
			joined++
		}
	}
	if joined == 0 {
		// Not fatal: unicast and broadcast still work, and those are the
		// layers that matter across subnets anyway.
		r.log.Debug("discovery: no interface joined the IPv4 multicast group")
	}
}

func (r *Responder) joinIPv6(pc net.PacketConn) {
	p := ipv6.NewPacketConn(pc)
	group := &net.UDPAddr{IP: MulticastGroupV6.AsSlice()}

	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagMulticast == 0 || ifaces[i].Flags&net.FlagUp == 0 {
			continue
		}
		_ = p.JoinGroup(&ifaces[i], group)
	}
}

func addrPortOf(addr net.Addr) (netip.AddrPort, bool) {
	switch a := addr.(type) {
	case *net.UDPAddr:
		ap := a.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), ap.IsValid()
	default:
		ap, err := netip.ParseAddrPort(addr.String())
		if err != nil {
			return netip.AddrPort{}, false
		}
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), true
	}
}
