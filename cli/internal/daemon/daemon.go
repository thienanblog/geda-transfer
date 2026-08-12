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

// Package daemon wires core/ into a long-running headless receiver.
//
// Everything it does is core's; this package chooses the state directory,
// starts the pieces together, and stops them together (AGENTS.md §2).
package daemon

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/geda/geda-transfer/core/discovery"
	"github.com/geda/geda-transfer/core/identity"
	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"

	"github.com/geda/geda-transfer/cli/internal/config"
	"github.com/geda/geda-transfer/cli/internal/control"
)

// settingDeviceID holds the identifier this receiver announces.
//
// It lives in the ledger rather than in the config file because it is not a
// preference: paired devices remember it, and a receiver that changed its id
// would look like a different machine to every phone that ever paired with it.
const settingDeviceID = "device_id"

// Daemon is a running headless receiver.
type Daemon struct {
	cfg     config.Config
	log     *slog.Logger
	version string

	db       *store.DB
	files    *storage.Store
	ident    *identity.Identity
	srv      *receiver.Server
	listener net.Listener

	deviceID  string
	startedAt time.Time
}

// New opens the state directory and prepares every component.
//
// The TLS listener is bound here rather than in Run so that a busy port fails
// before the ledger is touched, and so that a configured port of 0 -- which
// tests use -- is resolved to a real port that can be advertised.
func New(ctx context.Context, cfg config.Config, version string, log *slog.Logger) (*Daemon, error) {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare state directory: %w", err)
	}

	db, err := store.Open(ctx, filepath.Join(cfg.StateDir, "ledger.db"))
	if err != nil {
		return nil, err
	}

	d := &Daemon{cfg: cfg, log: log, version: version, db: db, startedAt: time.Now()}

	fail := func(err error) (*Daemon, error) {
		db.Close()
		return nil, err
	}

	if d.deviceID, err = deviceID(ctx, db); err != nil {
		return fail(err)
	}

	if cfg.NamingTemplate != "" {
		if err := applyTemplate(ctx, db, cfg.NamingTemplate); err != nil {
			return fail(err)
		}
	}

	if d.files, err = storage.New(db, cfg.Dest); err != nil {
		return fail(err)
	}

	// Outside the state directory would be wrong: the key must survive
	// updates and reinstalls, or every paired device sees a pin mismatch,
	// which is a hard failure with no override (AGENTS.md §3.5).
	if d.ident, err = identity.Load(filepath.Join(cfg.StateDir, "identity")); err != nil {
		return fail(err)
	}

	if d.listener, err = net.Listen("tcp", cfg.Listen); err != nil {
		return fail(fmt.Errorf("listen on %s: %w", cfg.Listen, err))
	}
	if d.cfg.AdvertisePort == 0 {
		d.cfg.AdvertisePort = d.listener.Addr().(*net.TCPAddr).Port
	}

	d.srv, err = receiver.New(receiver.Config{
		DeviceID:     d.deviceID,
		Name:         cfg.Name,
		DB:           db,
		Files:        d.files,
		Identity:     d.ident,
		TransferPort: d.cfg.AdvertisePort,
		Addrs:        cfg.Advertise,
		Logger:       log,
	})
	if err != nil {
		d.listener.Close()
		return fail(err)
	}

	return d, nil
}

// Close releases everything New acquired. Safe to call after Run returns.
func (d *Daemon) Close() error {
	if d.listener != nil {
		_ = d.listener.Close()
	}
	return d.db.Close()
}

// Addr is the address transfers are actually accepted on.
func (d *Daemon) Addr() net.Addr { return d.listener.Addr() }

// Fingerprint is the identity a user compares across two screens.
func (d *Daemon) Fingerprint() string { return d.ident.Fingerprint() }

// DeviceID is what this receiver announces.
func (d *Daemon) DeviceID() string { return d.deviceID }

// Run serves until ctx is cancelled.
//
// The receiver, the discovery responder, and the control socket run as one
// unit: if any of them fails the whole daemon stops, because a receiver that
// cannot be discovered, or that cannot be paired with, is not doing its job
// and silently half-working is the worst outcome for something with no UI.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, ctx := errgroup.WithContext(ctx)

	// Everything that can fail before anything is started is built first: a
	// misconfigured responder must not leave a receiver already serving on a
	// goroutine nobody is waiting for.
	var responder *discovery.Responder
	if d.cfg.Discovery {
		var err error
		responder, err = discovery.NewResponder(discovery.ResponderConfig{
			DeviceID:     d.deviceID,
			Name:         d.cfg.Name,
			Platform:     runtime.GOOS,
			TransferPort: d.cfg.AdvertisePort,
			Port:         d.cfg.DiscoveryPort,
			SPKI:         d.ident.Pin,
			Paired:       func() bool { n, err := d.countDevices(ctx); return err == nil && n > 0 },
			Candidates:   d.candidates,
			Logger:       d.log,
		})
		if err != nil {
			return err
		}
	}

	g.Go(func() error {
		if err := d.srv.Serve(ctx, d.listener); err != nil {
			return fmt.Errorf("receiver: %w", err)
		}
		return nil
	})

	if responder != nil {
		g.Go(func() error {
			if err := responder.Serve(ctx); err != nil {
				return fmt.Errorf("discovery: %w", err)
			}
			return nil
		})

		if d.cfg.MDNS {
			g.Go(func() error {
				// mDNS is the convenience layer, not the one that carries the
				// product: a box where 5353 is already taken by Avahi must
				// still receive files over the unicast layers.
				if err := responder.MDNS().Serve(ctx); err != nil {
					d.log.Warn("mDNS responder stopped; unicast discovery is unaffected", "error", err)
				}
				return nil
			})
		}
	}

	g.Go(func() error {
		if err := control.Serve(ctx, d.cfg.ControlSocket, d); err != nil {
			return fmt.Errorf("control socket: %w", err)
		}
		return nil
	})

	d.log.Info("gedad started",
		"version", d.version,
		"device_id", d.deviceID,
		"name", d.cfg.Name,
		"listen", d.listener.Addr().String(),
		"dest", d.files.Root(),
		"fingerprint", d.ident.Fingerprint())

	return g.Wait()
}

// candidates supplies the advertised address set.
//
// A configured list wins because the daemon cannot see the address peers reach
// it on: inside a bridged container the only local address is a private bridge
// address that nobody outside can dial.
func (d *Daemon) candidates() ([]string, error) {
	if len(d.cfg.Advertise) > 0 {
		return append([]string(nil), d.cfg.Advertise...), nil
	}
	return discovery.Candidates()
}

// Status implements control.Backend.
func (d *Daemon) Status(ctx context.Context) (control.Status, error) {
	addrs, err := d.candidateAddrs()
	if err != nil {
		return control.Status{}, err
	}

	devices, err := d.countDevices(ctx)
	if err != nil {
		return control.Status{}, err
	}

	var (
		files int
		bytes sql.NullInt64
	)
	if err := d.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(size) FROM files`).Scan(&files, &bytes); err != nil {
		return control.Status{}, fmt.Errorf("count files: %w", err)
	}

	return control.Status{
		Version:       d.version,
		DeviceID:      d.deviceID,
		Name:          d.cfg.Name,
		SPKI:          d.ident.Pin,
		Fingerprint:   d.ident.Fingerprint(),
		Dest:          d.files.Root(),
		StateDir:      d.cfg.StateDir,
		Listen:        d.listener.Addr().String(),
		Addrs:         addrs,
		StartedAt:     d.startedAt,
		PairedDevices: devices,
		Files:         files,
		Bytes:         bytes.Int64,
	}, nil
}

// Pair implements control.Backend.
func (d *Daemon) Pair(ctx context.Context, ttl time.Duration) (control.Offer, error) {
	offer, err := d.srv.BeginPairing(ttl)
	if err != nil {
		return control.Offer{}, err
	}
	return control.Offer{
		URI:         offer.URI,
		SPKI:        offer.Payload.SPKI,
		Fingerprint: identity.Fingerprint(offer.Payload.SPKI),
		Addrs:       offer.Payload.Addrs,
		ExpiresAt:   offer.ExpiresAt,
	}, nil
}

// Devices implements control.Backend.
func (d *Daemon) Devices(ctx context.Context) ([]control.Device, error) {
	rows, err := d.db.SQL().QueryContext(ctx, `
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

	var out []control.Device
	for rows.Next() {
		var (
			dev                 control.Device
			pairedAt            string
			lastSeen, revokedAt sql.NullString
		)
		if err := rows.Scan(&dev.ID, &dev.Name, &dev.Platform, &pairedAt,
			&lastSeen, &revokedAt, &dev.Files, &dev.Bytes); err != nil {
			return nil, fmt.Errorf("list devices: %w", err)
		}

		dev.PairedAt, _ = time.Parse(time.RFC3339Nano, pairedAt)
		if lastSeen.Valid {
			if t, err := time.Parse(time.RFC3339Nano, lastSeen.String); err == nil {
				dev.LastSeenAt = &t
			}
		}
		dev.Revoked = revokedAt.Valid
		out = append(out, dev)
	}
	return out, rows.Err()
}

// Unpair implements control.Backend.
func (d *Daemon) Unpair(ctx context.Context, deviceID string) error {
	return d.srv.Unpair(ctx, deviceID)
}

func (d *Daemon) candidateAddrs() ([]string, error) {
	hosts, err := d.candidates()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, net.JoinHostPort(h, fmt.Sprint(d.cfg.AdvertisePort)))
	}
	return out, nil
}

func (d *Daemon) countDevices(ctx context.Context) (int, error) {
	var n int
	err := d.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count devices: %w", err)
	}
	return n, nil
}

// deviceID reads the receiver's identifier, minting one on first run.
func deviceID(ctx context.Context, db *store.DB) (string, error) {
	id, err := db.Setting(ctx, settingDeviceID)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("read device id: %w", err)
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate device id: %w", err)
	}
	id = hex.EncodeToString(raw)

	if err := db.SetSetting(ctx, settingDeviceID, id); err != nil {
		return "", fmt.Errorf("save device id: %w", err)
	}
	return id, nil
}

// applyTemplate validates a configured naming template and stores it.
//
// Validation happens at startup rather than at the first upload: a template
// that cannot render is a config error, and finding out about it when the
// first photo of the holiday arrives is too late.
func applyTemplate(ctx context.Context, db *store.DB, tmpl string) error {
	if err := naming.Validate(tmpl); err != nil {
		return fmt.Errorf("naming_template: %w", err)
	}
	return db.SetSetting(ctx, storage.SettingTemplate, tmpl)
}
