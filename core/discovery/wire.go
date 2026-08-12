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

// Package discovery finds receivers on a network.
//
// Five layers run in parallel and their results are merged (AGENTS.md §3.4):
// mDNS, UDP broadcast/multicast, a unicast sweep over user-supplied CIDRs, a
// candidate-set retry for peers that are already paired, and manual entry.
//
// The unicast layers are not an optimisation. mDNS is multicast with TTL=1 and
// routers drop it by design, and a WireGuard tunnel is a point-to-point L3 link
// with no broadcast domain at all. Anything that has to cross a subnet
// boundary must therefore be unicast.
//
// Discovery is not a security boundary. It produces hints; nothing is trusted
// until a TLS handshake verifies against the pinned SPKI.
package discovery

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

const (
	// Version is the discovery wire version (docs/PROTOCOL.md §2).
	Version = 1

	// DefaultPort carries probes and announces.
	DefaultPort = 47890

	// DefaultTransferPort is the TCP port an announce advertises.
	DefaultTransferPort = 47891

	// MinProbeSize is the length a probe must reach before it is answered.
	//
	// This is an anti-amplification rule, not a formatting preference. An
	// unpadded ~60-byte probe carrying a spoofed source address would draw a
	// ~300-byte announce at the victim, turning every receiver into a 5x UDP
	// reflector. Padding the request past the size of the reply puts the
	// amplification factor below 1 and removes the incentive.
	MinProbeSize = 512

	// NonceTTL is how long a client remembers a nonce it sent. Announces
	// quoting anything else are discarded, which costs an off-path spoofer the
	// ability to inject peers into a scan.
	NonceTTL = 10 * time.Second

	// AnnounceInterval is the unsolicited announce period.
	AnnounceInterval = 30 * time.Second

	// AnnouncesPerSecond bounds replies to any one source address.
	AnnouncesPerSecond = 5
)

// Message types.
const (
	TypeProbe    = "probe"
	TypeAnnounce = "announce"
)

// MulticastGroupV4 and MulticastGroupV6 are the fixed groups probes are also
// sent to, for networks where broadcast is filtered but multicast is not.
var (
	MulticastGroupV4 = netip.MustParseAddr("239.192.71.90")
	MulticastGroupV6 = netip.MustParseAddr("ff12::7a90")
)

// maxDatagram bounds what is read from the wire. Announces are a few hundred
// bytes; probes are padded but never legitimately large.
const maxDatagram = 4096

// Probe asks any receiver that can hear it to announce itself.
type Probe struct {
	V     int    `json:"v"`
	T     string `json:"t"`
	Nonce string `json:"nonce"`
	Pad   string `json:"pad,omitempty"`
}

// Announce describes a receiver.
type Announce struct {
	V        int    `json:"v"`
	T        string `json:"t"`
	Nonce    string `json:"nonce,omitempty"`
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Port     int    `json:"port"`

	// SPKI is base64(SHA-256(SubjectPublicKeyInfo)). It is a display and
	// diagnostic hint here; trust comes from the pin recorded at pairing.
	SPKI string `json:"spki"`

	// Addrs is the candidate set: every address on every interface, VPN
	// addresses included. A paired client stores the whole set and races
	// connections across it, which is what lets a phone on a remote subnet
	// reach this receiver over WireGuard with no configuration.
	Addrs []string `json:"addrs"`

	// Paired reports whether this receiver has at least one paired device. It
	// is a UI hint only.
	Paired bool `json:"paired"`
}

// NewNonce returns a fresh 16-byte nonce, base64.
//
// One nonce covers one sweep round, not one host: a /24 sweep sends 254
// datagrams that all quote the same value, which is simpler to track and
// exactly as effective.
func NewNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("discovery: generate nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw[:]), nil
}

// MarshalProbe encodes a probe, padded to MinProbeSize.
func MarshalProbe(nonce string) ([]byte, error) {
	p := Probe{V: Version, T: TypeProbe, Nonce: nonce}

	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("discovery: encode probe: %w", err)
	}
	if len(raw) >= MinProbeSize {
		return raw, nil
	}

	// Padding is added as base64 of zero bytes so the datagram stays valid
	// JSON. Each 3 bytes of padding become 4 characters, and re-encoding adds
	// the field name and quotes, so the length is computed by growing the
	// padding until the encoded form is long enough rather than by arithmetic
	// that would have to be re-derived whenever a field is added.
	for n := (MinProbeSize - len(raw)) * 3 / 4; ; n += 8 {
		p.Pad = base64.StdEncoding.EncodeToString(make([]byte, n))
		raw, err = json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("discovery: encode probe: %w", err)
		}
		if len(raw) >= MinProbeSize {
			return raw, nil
		}
	}
}

// ErrNotProbe reports a datagram that is not a usable probe. Callers drop it
// without replying: an error reply would restore the amplification the padding
// rule exists to prevent.
var ErrNotProbe = errors.New("discovery: not a probe")

// ParseProbe decodes a probe and enforces the padding rule.
func ParseProbe(raw []byte) (Probe, error) {
	if len(raw) < MinProbeSize {
		return Probe{}, fmt.Errorf("%w: %d bytes, minimum %d", ErrNotProbe, len(raw), MinProbeSize)
	}

	var p Probe
	if err := json.Unmarshal(raw, &p); err != nil {
		return Probe{}, fmt.Errorf("%w: %v", ErrNotProbe, err)
	}
	if p.T != TypeProbe {
		return Probe{}, fmt.Errorf("%w: type %q", ErrNotProbe, p.T)
	}
	if p.V != Version {
		return Probe{}, fmt.Errorf("%w: version %d", ErrNotProbe, p.V)
	}
	if p.Nonce == "" {
		return Probe{}, fmt.Errorf("%w: no nonce", ErrNotProbe)
	}
	return p, nil
}

// ErrNotAnnounce reports a datagram that is not a usable announce.
var ErrNotAnnounce = errors.New("discovery: not an announce")

// ParseAnnounce decodes an announce.
func ParseAnnounce(raw []byte) (Announce, error) {
	var a Announce
	if err := json.Unmarshal(raw, &a); err != nil {
		return Announce{}, fmt.Errorf("%w: %v", ErrNotAnnounce, err)
	}
	if a.T != TypeAnnounce {
		return Announce{}, fmt.Errorf("%w: type %q", ErrNotAnnounce, a.T)
	}
	if a.V != Version {
		return Announce{}, fmt.Errorf("%w: version %d", ErrNotAnnounce, a.V)
	}
	if a.DeviceID == "" {
		return Announce{}, fmt.Errorf("%w: no device id", ErrNotAnnounce)
	}
	if a.Port <= 0 || a.Port > 65535 {
		return Announce{}, fmt.Errorf("%w: port %d", ErrNotAnnounce, a.Port)
	}
	return a, nil
}
