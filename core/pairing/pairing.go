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

// Package pairing carries the trust-on-first-use handshake: the QR payload a
// receiver shows, the single-use pre-shared key that authorises one pairing,
// and the per-device bearer token that results.
//
// There is no CA and no account. The user vouches for the receiver's public
// key by being physically in front of it and scanning its code; from then on
// the recorded SPKI pin is the whole of the trust relationship
// (docs/PROTOCOL.md §3).
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Version is the pairing payload version.
const Version = 1

// DefaultOfferTTL is how long a QR code stays valid. Long enough to walk
// across a room and unlock a phone, short enough that a photograph of a screen
// found later is worthless.
const DefaultOfferTTL = 5 * time.Minute

// URIScheme prefixes the QR payload so a camera app can route it.
const URIScheme = "geda://pair/"

// Payload is what the QR code encodes.
type Payload struct {
	V        int      `json:"v"`
	DeviceID string   `json:"device_id"`
	Name     string   `json:"name"`
	SPKI     string   `json:"spki"`
	Addrs    []string `json:"addrs"`
	PSK      string   `json:"psk"`
	Exp      int64    `json:"exp"`
}

// Encode renders the payload as base64url of compact JSON.
func (p Payload) Encode() (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("pairing: encode payload: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// URI renders the payload as the string to put in a QR code.
func (p Payload) URI() (string, error) {
	encoded, err := p.Encode()
	if err != nil {
		return "", err
	}
	return URIScheme + encoded, nil
}

// Expired reports whether the offer has run out.
func (p Payload) Expired(now time.Time) bool {
	return p.Exp != 0 && now.Unix() >= p.Exp
}

// ErrBadPayload reports a QR payload that cannot be used.
var ErrBadPayload = errors.New("pairing: unusable payload")

// Decode parses a scanned payload, with or without the URI scheme.
func Decode(s string) (Payload, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, URIScheme)

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return Payload{}, fmt.Errorf("%w: %v", ErrBadPayload, err)
	}

	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Payload{}, fmt.Errorf("%w: %v", ErrBadPayload, err)
	}
	switch {
	case p.V != Version:
		return Payload{}, fmt.Errorf("%w: version %d", ErrBadPayload, p.V)
	case p.DeviceID == "":
		return Payload{}, fmt.Errorf("%w: no device id", ErrBadPayload)
	case p.SPKI == "":
		// Without the pin there is nothing to trust on first use, and the
		// connection would be an unauthenticated one wearing a QR code.
		return Payload{}, fmt.Errorf("%w: no spki pin", ErrBadPayload)
	case p.PSK == "":
		return Payload{}, fmt.Errorf("%w: no psk", ErrBadPayload)
	case len(p.Addrs) == 0:
		return Payload{}, fmt.Errorf("%w: no addresses", ErrBadPayload)
	}
	return p, nil
}

// NewPSK returns a single-use pairing secret.
func NewPSK() (string, error) { return randomToken(32) }

// NewToken returns a device bearer token.
//
// 32 bytes because the token is the only credential after pairing: it is
// checked on every request, never rotated, and revoked only by unpairing.
func NewToken() (string, error) { return randomToken(32) }

func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("pairing: generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Offers holds the pairing secrets a receiver is currently willing to accept.
//
// Kept in memory only: an offer that survives a restart is a credential the
// user does not know is still outstanding.
type Offers struct {
	now func() time.Time

	mu   sync.Mutex
	live map[string]time.Time // sha256(psk) -> expiry
}

// NewOffers builds an empty offer set. now may be nil.
func NewOffers(now func() time.Time) *Offers {
	if now == nil {
		now = time.Now
	}
	return &Offers{now: now, live: make(map[string]time.Time)}
}

// Issue creates an offer and returns the secret. ttl of 0 means
// DefaultOfferTTL.
func (o *Offers) Issue(ttl time.Duration) (psk string, expires time.Time, err error) {
	if ttl <= 0 {
		ttl = DefaultOfferTTL
	}

	psk, err = NewPSK()
	if err != nil {
		return "", time.Time{}, err
	}
	expires = o.now().Add(ttl)

	o.mu.Lock()
	defer o.mu.Unlock()
	o.sweepLocked()
	o.live[fingerprint(psk)] = expires

	return psk, expires, nil
}

// Redeem consumes an offer, reporting whether it was live.
//
// The lookup is by SHA-256 of the secret rather than by comparing candidates
// one at a time: hashing first means the map probe reveals nothing about the
// secret through timing, and a caller cannot walk the offer set by measuring
// how long a wrong guess takes.
func (o *Offers) Redeem(psk string) bool {
	if psk == "" {
		return false
	}
	key := fingerprint(psk)

	o.mu.Lock()
	defer o.mu.Unlock()

	expiry, ok := o.live[key]
	if !ok {
		return false
	}
	// Single use, whether or not it had expired: a secret that has been
	// presented once is spent.
	delete(o.live, key)

	return o.now().Before(expiry)
}

// Revoke drops every outstanding offer, for a user who closes the pairing
// screen.
func (o *Offers) Revoke() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.live = make(map[string]time.Time)
}

// Len reports how many offers are live, for tests and diagnostics.
func (o *Offers) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sweepLocked()
	return len(o.live)
}

func (o *Offers) sweepLocked() {
	now := o.now()
	for key, expiry := range o.live {
		if !now.Before(expiry) {
			delete(o.live, key)
		}
	}
}

func fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
