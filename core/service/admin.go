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

package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/geda/geda-transfer/core/identity"
	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/storage"
)

// Status is a snapshot of what a receiver is and what it holds.
//
// The JSON tags are the wire format of gedad's control socket, so they are not
// free to change: a `gedad status -json` in somebody's monitoring script reads
// them.
type Status struct {
	Version       string    `json:"version"`
	DeviceID      string    `json:"device_id"`
	Name          string    `json:"name"`
	SPKI          string    `json:"spki"`
	Fingerprint   string    `json:"fingerprint"`
	Dest          string    `json:"dest"`
	StateDir      string    `json:"state_dir"`
	Listen        string    `json:"listen"`
	Addrs         []string  `json:"addrs"`
	StartedAt     time.Time `json:"started_at"`
	PairedDevices int       `json:"paired_devices"`
	Files         int       `json:"files"`
	Bytes         int64     `json:"bytes"`
}

// Offer is a pairing invitation, ready to be drawn as a QR code.
type Offer struct {
	URI         string    `json:"uri"`
	SPKI        string    `json:"spki"`
	Fingerprint string    `json:"fingerprint"`
	Addrs       []string  `json:"addrs"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Device is one paired device and what it has sent.
type Device struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Platform   string     `json:"platform"`
	PairedAt   time.Time  `json:"paired_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	Revoked    bool       `json:"revoked"`
	Files      int        `json:"files"`
	Bytes      int64      `json:"bytes"`
}

// HistoryEntry is one received file, as a history list shows it.
type HistoryEntry struct {
	ID         int64      `json:"id"`
	DeviceID   string     `json:"device_id"`
	DeviceName string     `json:"device_name"`
	Name       string     `json:"name"`
	StoredPath string     `json:"stored_path"`
	Kind       string     `json:"kind"`
	Size       int64      `json:"size"`
	Hash       string     `json:"hash"`
	CapturedAt *time.Time `json:"captured_at,omitempty"`
	ReceivedAt time.Time  `json:"received_at"`
}

// HistoryQuery narrows a history listing.
type HistoryQuery struct {
	// DeviceID, when set, limits the listing to one device.
	DeviceID string

	// Limit caps the number of rows. Zero means DefaultHistoryLimit.
	Limit int

	// Before, when set, returns only files received strictly before it. This
	// is how a UI pages: the caller passes the ReceivedAt of the last row it
	// has, rather than an offset that shifts as new files arrive.
	Before time.Time
}

// DefaultHistoryLimit is one screenful and then some.
const DefaultHistoryLimit = 200

// MaxHistoryLimit bounds one query. A window cannot usefully show more, and an
// unbounded limit turns a UI refresh into a full table scan on a NAS holding
// years of photos.
const MaxHistoryLimit = 2000

// Status reports what this receiver is and what it holds.
func (s *Service) Status(ctx context.Context) (Status, error) {
	addrs, err := s.CandidateAddrs()
	if err != nil {
		return Status{}, err
	}

	devices, err := s.countDevices(ctx)
	if err != nil {
		return Status{}, err
	}

	var (
		files int
		bytes sql.NullInt64
	)
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(size) FROM files`).Scan(&files, &bytes); err != nil {
		return Status{}, fmt.Errorf("count files: %w", err)
	}

	return Status{
		Version:       s.cfg.Version,
		DeviceID:      s.deviceID,
		Name:          s.cfg.Name,
		SPKI:          s.ident.Pin,
		Fingerprint:   s.ident.Fingerprint(),
		Dest:          s.files.Root(),
		StateDir:      s.cfg.StateDir,
		Listen:        s.listener.Addr().String(),
		Addrs:         addrs,
		StartedAt:     s.startedAt,
		PairedDevices: devices,
		Files:         files,
		Bytes:         bytes.Int64,
	}, nil
}

// Pair issues a single-use pairing offer valid for ttl.
//
// Zero ttl means pairing.DefaultOfferTTL. The offer lives in memory and is
// spent when it is redeemed, so a code that has been photographed off a screen
// cannot be used twice (docs/DECISIONS.md).
func (s *Service) Pair(ctx context.Context, ttl time.Duration) (Offer, error) {
	offer, err := s.srv.BeginPairing(ttl)
	if err != nil {
		return Offer{}, err
	}
	return Offer{
		URI:         offer.URI,
		SPKI:        offer.Payload.SPKI,
		Fingerprint: identity.Fingerprint(offer.Payload.SPKI),
		Addrs:       offer.Payload.Addrs,
		ExpiresAt:   offer.ExpiresAt,
	}, nil
}

// CancelPairing withdraws the outstanding offer, if any.
//
// The desktop calls this when the pairing window closes. An offer left
// outstanding is a credential the user believes they have put away.
func (s *Service) CancelPairing() { s.srv.CancelPairing() }

// Devices lists every device that has paired, revoked ones included.
//
// Revoked devices are kept and shown because their files are still on disk and
// still attributed to them; hiding the row would leave a folder full of photos
// with no explanation of where it came from.
func (s *Service) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT d.id, d.name, d.platform, d.paired_at, d.last_seen_at, d.revoked_at,
		       COUNT(f.id), COALESCE(SUM(f.size), 0)
		FROM devices d
		LEFT JOIN files f ON f.device_id = d.id
		GROUP BY d.id
		ORDER BY d.paired_at`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		var (
			dev                 Device
			pairedAt            string
			lastSeen, revokedAt sql.NullString
		)
		if err := rows.Scan(&dev.ID, &dev.Name, &dev.Platform, &pairedAt,
			&lastSeen, &revokedAt, &dev.Files, &dev.Bytes); err != nil {
			return nil, fmt.Errorf("list devices: %w", err)
		}

		dev.PairedAt, _ = time.Parse(time.RFC3339Nano, pairedAt)
		dev.LastSeenAt = nullTime(lastSeen)
		dev.Revoked = revokedAt.Valid
		out = append(out, dev)
	}
	return out, rows.Err()
}

// Unpair revokes a device's token. Its files stay where they are.
func (s *Service) Unpair(ctx context.Context, deviceID string) error {
	return s.srv.Unpair(ctx, deviceID)
}

// History lists received files, newest first.
func (s *Service) History(ctx context.Context, q HistoryQuery) ([]HistoryEntry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	if limit > MaxHistoryLimit {
		limit = MaxHistoryLimit
	}

	// Built by hand rather than with a fixed WHERE that is always true,
	// because SQLite chooses a different plan for the device-scoped case and
	// the index is there to be used.
	query := `
		SELECT f.id, f.device_id, d.name, f.original_name, f.stored_path,
		       f.kind, f.size, f.hash, f.captured_at, f.received_at
		FROM files f
		JOIN devices d ON d.id = f.device_id`
	var (
		where []string
		args  []any
	)
	if q.DeviceID != "" {
		where = append(where, "f.device_id = ?")
		args = append(args, q.DeviceID)
	}
	if !q.Before.IsZero() {
		where = append(where, "f.received_at < ?")
		args = append(args, q.Before.UTC().Format(time.RFC3339Nano))
	}
	for i, clause := range where {
		if i == 0 {
			query += " WHERE " + clause
		} else {
			query += " AND " + clause
		}
	}
	query += " ORDER BY f.received_at DESC, f.id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	defer rows.Close()

	out := make([]HistoryEntry, 0, limit)
	for rows.Next() {
		var (
			e          HistoryEntry
			capturedAt sql.NullString
			receivedAt string
		)
		if err := rows.Scan(&e.ID, &e.DeviceID, &e.DeviceName, &e.Name, &e.StoredPath,
			&e.Kind, &e.Size, &e.Hash, &capturedAt, &receivedAt); err != nil {
			return nil, fmt.Errorf("list history: %w", err)
		}
		e.CapturedAt = nullTime(capturedAt)
		e.ReceivedAt, _ = time.Parse(time.RFC3339Nano, receivedAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Template is the filename template in force, or the default if none is set.
func (s *Service) Template(ctx context.Context) (string, error) {
	return s.files.Template(ctx)
}

// SetTemplate validates a filename template and stores it.
//
// An invalid template is rejected here rather than at the next upload: `{yyy}`
// renders literally, so a typo would name every future file after the typo and
// nobody would notice until the photos were filed under it.
func (s *Service) SetTemplate(ctx context.Context, tmpl string) error {
	return applyTemplate(ctx, s.db, tmpl)
}

// TemplatePreview renders a template against a representative asset, so a
// settings screen can show the effect of an edit before it is saved.
//
// It returns the rendered path and an error for a template that cannot work.
func TemplatePreview(tmpl string) (string, error) {
	if err := naming.Validate(tmpl); err != nil {
		return "", err
	}
	res, err := naming.Render(tmpl, naming.Vars{
		CapturedAt:   time.Date(2026, 7, 4, 15, 9, 3, 0, time.UTC),
		OriginalName: "IMG_4021.HEIC",
		Device:       "An's iPhone",
		Album:        "Summer",
		Hash:         "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
	}, 0)
	if err != nil {
		return "", err
	}
	return res.Path(), nil
}

// AbsPath turns a stored path from the ledger into an absolute one, for a
// "show in Finder" that has to hand the OS a real location.
func (s *Service) AbsPath(storedPath string) string {
	return s.files.AbsPath(storage.Committed{Path: storedPath})
}
