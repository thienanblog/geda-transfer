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
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// L1: mDNS. This layer only ever reaches the local segment -- multicast DNS is
// sent with TTL=1 and routers drop it by design -- so it is a convenience for
// the common case, never the mechanism that crosses a subnet. That is L3/L4.

const (
	// ServiceName is the DNS-SD service type (docs/PROTOCOL.md §1).
	ServiceName = "_gedatransfer._tcp.local."

	mdnsPort = 5353

	// mdnsTTL is the record lifetime advertised to peers. 120s is the
	// DNS-SD convention for records tied to a running process.
	mdnsTTL = 120
)

var (
	mdnsGroupV4 = netip.MustParseAddr("224.0.0.251")
	mdnsGroupV6 = netip.MustParseAddr("ff02::fb")
)

// MDNSResponder answers `_gedatransfer._tcp` queries on the local segment.
type MDNSResponder struct {
	r *Responder
}

// MDNS returns the mDNS half of this responder.
func (r *Responder) MDNS() *MDNSResponder { return &MDNSResponder{r: r} }

// Serve answers mDNS queries until ctx is done.
//
// Conflict probing (RFC 6762 §8) is deliberately not implemented: the instance
// name is qualified with the device id, which is a UUID, so two receivers
// cannot claim the same name in the first place.
func (m *MDNSResponder) Serve(ctx context.Context) error {
	// Shared: the platform's own mDNS responder already holds 5353, and
	// refusing to share it would mean never answering at all.
	pc, err := listenUDP(ctx, "udp4", mdnsPort, true)
	if err != nil {
		return fmt.Errorf("discovery: listen mdns: %w", err)
	}
	defer pc.Close()

	p := ipv4.NewPacketConn(pc)
	group := &net.UDPAddr{IP: mdnsGroupV4.AsSlice()}
	if ifaces, err := net.Interfaces(); err == nil {
		for i := range ifaces {
			if ifaces[i].Flags&net.FlagMulticast == 0 || ifaces[i].Flags&net.FlagUp == 0 {
				continue
			}
			_ = p.JoinGroup(&ifaces[i], group)
		}
	}

	go func() {
		<-ctx.Done()
		pc.Close()
	}()

	buf := make([]byte, maxDatagram)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("discovery: mdns read: %w", err)
		}
		m.handleQuery(pc, buf[:n], from)
	}
}

func (m *MDNSResponder) handleQuery(pc net.PacketConn, raw []byte, from net.Addr) {
	var parser dnsmessage.Parser
	header, err := parser.Start(raw)
	if err != nil || header.Response {
		return
	}

	wanted := false
	instance := m.instanceName()
	for {
		q, err := parser.Question()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return
		}

		name := strings.ToLower(q.Name.String())
		switch {
		case name == ServiceName && (q.Type == dnsmessage.TypePTR || q.Type == dnsmessage.TypeALL):
			wanted = true
		case name == strings.ToLower(instance):
			wanted = true
		case name == strings.ToLower(m.hostName()):
			wanted = true
		}
	}
	if !wanted {
		return
	}

	reply, err := m.buildResponse()
	if err != nil {
		m.r.log.Debug("discovery: could not build mdns response", "error", err)
		return
	}

	// Answer both ways: multicast so that other listeners refresh their
	// caches, and unicast so the asker gets it even where multicast reception
	// is flaky.
	_, _ = pc.WriteTo(reply, &net.UDPAddr{IP: mdnsGroupV4.AsSlice(), Port: mdnsPort})
	if from != nil {
		_, _ = pc.WriteTo(reply, from)
	}
}

func (m *MDNSResponder) instanceName() string {
	return fmt.Sprintf("%s.%s", mdnsLabel(m.r.cfg.Name, m.r.cfg.DeviceID), ServiceName)
}

func (m *MDNSResponder) hostName() string {
	return fmt.Sprintf("%s.local.", mdnsLabel(m.r.cfg.Name, m.r.cfg.DeviceID))
}

// mdnsLabel builds a label that is unique without a conflict-resolution
// protocol: the human name, then a slice of the device id.
func mdnsLabel(name, deviceID string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		case r == ' ' || r == '_' || r == '.':
			return '-'
		default:
			return -1
		}
	}, name)
	clean = strings.Trim(clean, "-")
	if clean == "" {
		clean = "geda"
	}
	if len(clean) > 40 {
		clean = clean[:40]
	}

	suffix := strings.ReplaceAll(deviceID, "-", "")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return clean + "-" + suffix
}

func (m *MDNSResponder) buildResponse() ([]byte, error) {
	addrs, err := m.r.candidates()
	if err != nil {
		return nil, err
	}

	instance, err := dnsmessage.NewName(m.instanceName())
	if err != nil {
		return nil, err
	}
	service, err := dnsmessage.NewName(ServiceName)
	if err != nil {
		return nil, err
	}
	host, err := dnsmessage.NewName(m.hostName())
	if err != nil {
		return nil, err
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	b.EnableCompression()
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}

	ptrHeader := dnsmessage.ResourceHeader{Name: service, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET, TTL: mdnsTTL}
	if err := b.PTRResource(ptrHeader, dnsmessage.PTRResource{PTR: instance}); err != nil {
		return nil, err
	}

	srvHeader := dnsmessage.ResourceHeader{Name: instance, Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET, TTL: mdnsTTL}
	if err := b.SRVResource(srvHeader, dnsmessage.SRVResource{
		Port:   uint16(m.r.cfg.TransferPort),
		Target: host,
	}); err != nil {
		return nil, err
	}

	txtHeader := dnsmessage.ResourceHeader{Name: instance, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: mdnsTTL}
	if err := b.TXTResource(txtHeader, dnsmessage.TXTResource{TXT: m.txtRecords()}); err != nil {
		return nil, err
	}

	for _, raw := range addrs {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			continue
		}
		if addr.Is4() {
			h := dnsmessage.ResourceHeader{Name: host, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: mdnsTTL}
			if err := b.AResource(h, dnsmessage.AResource{A: addr.As4()}); err != nil {
				return nil, err
			}
			continue
		}
		h := dnsmessage.ResourceHeader{Name: host, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: mdnsTTL}
		if err := b.AAAAResource(h, dnsmessage.AAAAResource{AAAA: addr.As16()}); err != nil {
			return nil, err
		}
	}

	return b.Finish()
}

func (m *MDNSResponder) txtRecords() []string {
	txt := []string{
		"v=" + strconv.Itoa(Version),
		"id=" + m.r.cfg.DeviceID,
		"name=" + m.r.cfg.Name,
		"platform=" + m.r.cfg.Platform,
	}
	if m.r.cfg.SPKI != "" {
		txt = append(txt, "spki="+m.r.cfg.SPKI)
	}
	if m.r.cfg.Paired != nil && m.r.cfg.Paired() {
		txt = append(txt, "paired=1")
	}
	return txt
}

// browseMDNS runs L1 for a scan: query the service and report what answers.
func (c *collector) browseMDNS(ctx context.Context) {
	pc, err := listenSender(ctx, "udp4")
	if err != nil {
		c.cfg.Logger.Debug("discovery: mdns browse unavailable", "error", err)
		return
	}
	defer pc.Close()

	query, err := mdnsQuery()
	if err != nil {
		c.cfg.Logger.Debug("discovery: could not build mdns query", "error", err)
		return
	}

	go func() {
		<-ctx.Done()
		pc.Close()
	}()

	go func() {
		dst := &net.UDPAddr{IP: mdnsGroupV4.AsSlice(), Port: mdnsPort}
		for round := 0; round < c.cfg.Rounds; round++ {
			if _, err := pc.WriteTo(query, dst); err != nil {
				c.cfg.Logger.Debug("discovery: mdns query failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(c.cfg.RoundInterval):
			}
		}
	}()

	buf := make([]byte, maxDatagram)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		src, ok := addrPortOf(from)
		if !ok {
			continue
		}
		for _, a := range parseMDNSResponse(buf[:n]) {
			c.record(a, src, SourceMDNS)
		}
	}
}

func mdnsQuery() ([]byte, error) {
	name, err := dnsmessage.NewName(ServiceName)
	if err != nil {
		return nil, err
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{
		Name:  name,
		Type:  dnsmessage.TypePTR,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// parseMDNSResponse turns one mDNS message into announces.
//
// Records describing one service arrive spread across the answer and
// additional sections in no fixed order, so everything is collected first and
// assembled afterwards.
func parseMDNSResponse(raw []byte) []Announce {
	var parser dnsmessage.Parser
	header, err := parser.Start(raw)
	if err != nil || !header.Response {
		return nil
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return nil
	}

	c := mdnsCollected{
		instances: map[string]*mdnsInstance{},
		hosts:     map[string][]string{},
	}

	for {
		h, err := parser.AnswerHeader()
		if err != nil {
			break
		}
		if !c.take(&parser, h) {
			if err := parser.SkipAnswer(); err != nil {
				break
			}
		}
	}

	if err := parser.SkipAllAuthorities(); err == nil {
		for {
			h, err := parser.AdditionalHeader()
			if err != nil {
				break
			}
			if !c.take(&parser, h) {
				if err := parser.SkipAdditional(); err != nil {
					break
				}
			}
		}
	}

	return c.announces()
}

type mdnsInstance struct {
	host string
	port uint16
	txt  map[string]string
}

type mdnsCollected struct {
	instances map[string]*mdnsInstance
	hosts     map[string][]string
}

func (c mdnsCollected) instance(name string) *mdnsInstance {
	key := strings.ToLower(name)
	rec, ok := c.instances[key]
	if !ok {
		rec = &mdnsInstance{txt: map[string]string{}}
		c.instances[key] = rec
	}
	return rec
}

// take consumes the resource body when the type is one this protocol uses, and
// reports whether it did. Anything else is left for the caller to skip.
func (c mdnsCollected) take(parser *dnsmessage.Parser, h dnsmessage.ResourceHeader) bool {
	switch h.Type {
	case dnsmessage.TypePTR:
		ptr, err := parser.PTRResource()
		if err == nil {
			c.instance(ptr.PTR.String())
		}
		return true

	case dnsmessage.TypeSRV:
		srv, err := parser.SRVResource()
		if err == nil {
			rec := c.instance(h.Name.String())
			rec.host = srv.Target.String()
			rec.port = srv.Port
		}
		return true

	case dnsmessage.TypeTXT:
		txt, err := parser.TXTResource()
		if err == nil {
			rec := c.instance(h.Name.String())
			for _, entry := range txt.TXT {
				if k, v, ok := strings.Cut(entry, "="); ok {
					rec.txt[k] = v
				}
			}
		}
		return true

	case dnsmessage.TypeA:
		a, err := parser.AResource()
		if err == nil {
			c.addHost(h.Name.String(), netip.AddrFrom4(a.A))
		}
		return true

	case dnsmessage.TypeAAAA:
		aaaa, err := parser.AAAAResource()
		if err == nil {
			c.addHost(h.Name.String(), netip.AddrFrom16(aaaa.AAAA).Unmap())
		}
		return true
	}
	return false
}

func (c mdnsCollected) addHost(name string, addr netip.Addr) {
	if !usableAddr(addr) {
		return
	}
	key := strings.ToLower(name)
	for _, existing := range c.hosts[key] {
		if existing == addr.String() {
			return
		}
	}
	c.hosts[key] = append(c.hosts[key], addr.String())
}

func (c mdnsCollected) announces() []Announce {
	var out []Announce
	for name, rec := range c.instances {
		if rec.port == 0 || rec.txt["id"] == "" {
			// A bare PTR with no SRV yet is a pointer to nothing usable.
			continue
		}
		out = append(out, Announce{
			V:        Version,
			T:        TypeAnnounce,
			DeviceID: rec.txt["id"],
			Name:     displayName(rec.txt["name"], name),
			Platform: rec.txt["platform"],
			Port:     int(rec.port),
			SPKI:     rec.txt["spki"],
			Addrs:    c.hosts[strings.ToLower(rec.host)],
			Paired:   rec.txt["paired"] == "1",
		})
	}
	return out
}

func displayName(fromTXT, instance string) string {
	if fromTXT != "" {
		return fromTXT
	}
	label, _, _ := strings.Cut(instance, ".")
	return label
}
