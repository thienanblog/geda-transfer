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

package receiver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/geda/geda-transfer/core/discovery"
	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/pairing"
)

// MaxConcurrency is what a client is told to use for parallel uploads. Six to
// eight streams over one HTTP/2 connection saturates a Wi-Fi 6 link without
// making the receiver's disk seek itself to death.
const MaxConcurrency = 8

// Offer is a pairing invitation, ready to be rendered as a QR code.
type Offer struct {
	Payload   pairing.Payload
	URI       string
	ExpiresAt time.Time
}

// BeginPairing issues a single-use pairing offer.
//
// The payload carries the whole candidate address set, tunnel addresses
// included. That is what lets a phone which pairs on the LAN today still reach
// this receiver from another subnet tomorrow, without the user configuring
// anything (AGENTS.md §3.4).
func (s *Server) BeginPairing(ttl time.Duration) (Offer, error) {
	psk, expires, err := s.offers.Issue(ttl)
	if err != nil {
		return Offer{}, err
	}

	addrs, err := s.candidateAddrs()
	if err != nil {
		return Offer{}, err
	}

	payload := pairing.Payload{
		V:        pairing.Version,
		DeviceID: s.cfg.DeviceID,
		Name:     s.cfg.Name,
		SPKI:     s.cfg.Identity.Pin,
		Addrs:    addrs,
		PSK:      psk,
		Exp:      expires.Unix(),
	}

	uri, err := payload.URI()
	if err != nil {
		return Offer{}, err
	}
	return Offer{Payload: payload, URI: uri, ExpiresAt: expires}, nil
}

// CancelPairing drops every outstanding offer, for a user closing the pairing
// screen.
func (s *Server) CancelPairing() { s.offers.Revoke() }

// candidateAddrs renders the local addresses as host:port entries a client can
// dial directly.
func (s *Server) candidateAddrs() ([]string, error) {
	addrs := s.cfg.Addrs
	if addrs == nil {
		var err error
		if addrs, err = discovery.Candidates(); err != nil {
			return nil, fmt.Errorf("receiver: enumerate addresses: %w", err)
		}
	}

	port := s.cfg.TransferPort
	if port == 0 {
		port = discovery.DefaultTransferPort
	}

	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, joinHostPort(a, port))
	}
	return out, nil
}

// pairRequest is docs/PROTOCOL.md §3.2.
type pairRequest struct {
	V        int    `json:"v"`
	PSK      string `json:"psk"`
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`

	// SPKI is the client's own pin, when it has a TLS identity of its own.
	// Unused today -- clients authenticate with a bearer token, not a
	// certificate -- and recorded so that turning on mutual TLS later does not
	// require everyone to re-pair.
	SPKI string `json:"spki,omitempty"`
}

type pairResponse struct {
	Token          string   `json:"token"`
	DeviceID       string   `json:"device_id"`
	Name           string   `json:"name"`
	SPKI           string   `json:"spki"`
	Addrs          []string `json:"addrs"`
	NamingTemplate string   `json:"naming_template"`
	MaxConcurrency int      `json:"max_concurrency"`
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be JSON", false)
		return
	}
	if req.V != pairing.Version {
		writeError(w, http.StatusBadRequest, "unsupported_version",
			fmt.Sprintf("this receiver speaks pairing version %d", pairing.Version), false)
		return
	}
	if req.DeviceID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "device_id and name are required", false)
		return
	}

	// Redeeming is the authorisation step: the secret is single-use, so a
	// replay of this request cannot pair a second device.
	if !s.offers.Redeem(req.PSK) {
		writeError(w, http.StatusUnauthorized, "unauthorized",
			"this pairing code is not valid; show a fresh QR code on the receiver", false)
		return
	}

	token, err := pairing.NewToken()
	if err != nil {
		s.log.Error("could not generate device token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not complete pairing", true)
		return
	}

	if err := s.savePairedDevice(r.Context(), req, token); err != nil {
		s.log.Error("could not record paired device", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not complete pairing", true)
		return
	}

	addrs, err := s.candidateAddrs()
	if err != nil {
		// The device is paired; failing the response now would leave the
		// client without the token it has already been granted.
		s.log.Warn("could not enumerate addresses for pairing response", "error", err)
	}

	template, err := s.cfg.Files.Template(r.Context())
	if err != nil {
		s.log.Warn("could not read naming template", "error", err)
		template = naming.Default
	}

	s.log.Info("device paired", "device_id", req.DeviceID, "name", req.Name, "platform", req.Platform)

	writeJSON(w, http.StatusOK, pairResponse{
		Token:          token,
		DeviceID:       s.cfg.DeviceID,
		Name:           s.cfg.Name,
		SPKI:           s.cfg.Identity.Pin,
		Addrs:          addrs,
		NamingTemplate: template,
		MaxConcurrency: MaxConcurrency,
	})
}

// savePairedDevice records the device, or re-arms one that is pairing again.
//
// Re-pairing keeps the row rather than replacing it: the files table points at
// it, and a user who re-scans a QR code after reinstalling the app expects
// their history to still be there.
//
// A client chooses its own device_id, so one that presents an id already in
// the table takes over that device's folder and revokes its token. What bounds
// that is the pairing secret: it is single-use and comes off a QR code
// displayed on the receiver, so claiming another device's identity requires
// standing in front of the machine -- at which point the user is doing it on
// purpose.
func (s *Server) savePairedDevice(ctx context.Context, req pairRequest, token string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := s.cfg.DB.SQL().ExecContext(ctx, `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name       = excluded.name,
			platform   = excluded.platform,
			spki_pin   = excluded.spki_pin,
			token_hash = excluded.token_hash,
			paired_at  = excluded.paired_at,
			revoked_at = NULL`,
		req.DeviceID, req.Name, req.Platform, req.SPKI, HashToken(token), now)
	if err != nil {
		return fmt.Errorf("receiver: save paired device: %w", err)
	}
	return nil
}

// Unpair revokes a device's token. The files it sent are left alone: they are
// the user's, and deleting them is a separate, explicit action.
func (s *Server) Unpair(ctx context.Context, deviceID string) error {
	res, err := s.cfg.DB.SQL().ExecContext(ctx,
		`UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), deviceID)
	if err != nil {
		return fmt.Errorf("receiver: unpair %s: %w", deviceID, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("receiver: unpair %s: %w", deviceID, err)
	}
	if n == 0 {
		return fmt.Errorf("receiver: unpair %s: %w", deviceID, ErrUnknownDevice)
	}

	s.forgetDevice(deviceID)
	return nil
}

// ErrUnknownDevice reports an unpair request for a device that is not paired.
var ErrUnknownDevice = errors.New("unknown device")
