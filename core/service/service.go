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

// Package service assembles core's pieces into one running receiver.
//
// A receiver is the ledger, the TLS identity, the storage layout, the HTTP
// server, and the discovery responders, started together and stopped together.
// Every front end needs exactly that: `gedad` on a NAS, the Wails app on a
// desktop, and the Docker image wrapping the first of them.
//
// It lives in core/ because it is behaviour, not presentation (AGENTS.md §2).
// The alternative -- one copy of this wiring in cli/ and another in desktop/ --
// is how the two ends of the product drift apart: a fix to the discovery
// lifecycle that lands on the NAS and not on the desktop, or a state directory
// that means something slightly different depending on which binary made it.
//
// What is *not* here is anything about how a user is asked: config files and
// control sockets belong to cli/, windows and menus to desktop/.
package service

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
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/geda/geda-transfer/core/discovery"
	"github.com/geda/geda-transfer/core/events"
	"github.com/geda/geda-transfer/core/formats"
	"github.com/geda/geda-transfer/core/identity"
	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"
)

// SettingDeviceID holds the identifier this receiver announces.
//
// It lives in the ledger rather than in any configuration file because it is
// not a preference: paired devices remember it, and a receiver that changed
// its id would look like a different machine to every phone that ever paired.
const SettingDeviceID = "device_id"

// Config describes a receiver to assemble.
//
// Only StateDir is required; everything else has a working default, because
// the product has to run with zero configuration (AGENTS.md §4).
type Config struct {
	// Name is what users see when choosing a destination. Defaults to the
	// hostname.
	Name string

	// Version is reported in Status. Front ends stamp their own.
	Version string

	// StateDir holds the ledger and the TLS identity. It must survive
	// updates: losing the identity key makes every paired device fail with a
	// pin mismatch, which has no override (AGENTS.md §3.5).
	StateDir string

	// Dest is where received files are written. Defaults to StateDir/Photos.
	Dest string

	// Listen is the TLS listen address for transfers. Defaults to every
	// interface on discovery.DefaultTransferPort. A port of 0 asks the OS for
	// a free one, which Open then resolves so it can be advertised.
	Listen string

	// DiscoveryPort is the UDP port probes arrive on.
	DiscoveryPort int

	// AdvertisePort is the TCP port peers are told to dial. It differs from
	// the listen port only behind a port mapping.
	AdvertisePort int

	// Advertise overrides the candidate address set. Empty means every local
	// interface address, tunnels included, which is what a desktop and a
	// host-network container both want.
	Advertise []string

	// MDNS runs the L1 responder, Discovery the UDP responder (L2-L5).
	MDNS      bool
	Discovery bool

	// NamingTemplate, when set, is written to the ledger at startup. A front
	// end whose settings live in a file makes that file the authority; one
	// whose settings live in the ledger leaves this empty.
	NamingTemplate string

	// OutputPolicy, when set, is written to the ledger at startup, on the
	// same terms as NamingTemplate. Nil leaves whatever is stored alone.
	OutputPolicy *formats.Policy

	// ConversionWorkers overrides how many files are converted at once. Zero
	// picks a number from the machine.
	ConversionWorkers int

	// Logger receives operational messages. Defaults to slog.Default().
	Logger *slog.Logger

	// Events, when set, receives the lifecycle of every upload.
	Events *events.Bus
}

// Service is an assembled receiver. Open builds one; Run serves it.
type Service struct {
	cfg Config
	log *slog.Logger

	db          *store.DB
	files       *storage.Store
	ident       *identity.Identity
	srv         *receiver.Server
	listener    net.Listener
	conversions *formats.Queue
	tools       formats.Tools

	deviceID  string
	startedAt time.Time
}

// Open prepares the state directory and every component, and binds the
// listener.
//
// The listener is bound here rather than in Run so that a port already in use
// fails before the ledger is touched, and so that a configured port of 0 is
// resolved to a real port that can be advertised.
func Open(ctx context.Context, cfg Config) (*Service, error) {
	if err := cfg.resolve(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare state directory: %w", err)
	}

	db, err := store.Open(ctx, filepath.Join(cfg.StateDir, "ledger.db"))
	if err != nil {
		return nil, err
	}

	s := &Service{cfg: cfg, log: cfg.Logger, db: db, startedAt: time.Now()}

	fail := func(err error) (*Service, error) {
		db.Close()
		return nil, err
	}

	if s.deviceID, err = deviceID(ctx, db); err != nil {
		return fail(err)
	}

	if cfg.NamingTemplate != "" {
		if err := applyTemplate(ctx, db, cfg.NamingTemplate); err != nil {
			return fail(err)
		}
	}

	if cfg.OutputPolicy != nil {
		if err := applyPolicy(ctx, db, *cfg.OutputPolicy); err != nil {
			return fail(err)
		}
	}

	if s.files, err = storage.New(db, cfg.Dest); err != nil {
		return fail(err)
	}

	// The external converters are located once, at startup, rather than per
	// file: a PATH search and three version probes per received photo would
	// be a syscall storm on a library import, and a tool installed while the
	// app is running is a restart away from being noticed.
	s.tools = formats.Detect(ctx)

	s.conversions, err = formats.NewQueue(formats.QueueConfig{
		DB:        db,
		Root:      s.files.Root(),
		Converter: formats.NewConverter(s.tools),
		Workers:   cfg.ConversionWorkers,
		Logger:    cfg.Logger,
	})
	if err != nil {
		return fail(err)
	}
	s.files.NotifyConversions(s.conversions.Wake)

	if s.ident, err = identity.Load(filepath.Join(cfg.StateDir, "identity")); err != nil {
		return fail(err)
	}

	if s.listener, err = net.Listen("tcp", cfg.Listen); err != nil {
		return fail(fmt.Errorf("listen on %s: %w", cfg.Listen, err))
	}
	if s.cfg.AdvertisePort == 0 {
		s.cfg.AdvertisePort = s.listener.Addr().(*net.TCPAddr).Port
	}

	s.srv, err = receiver.New(receiver.Config{
		DeviceID:     s.deviceID,
		Name:         cfg.Name,
		DB:           db,
		Files:        s.files,
		Identity:     s.ident,
		TransferPort: s.cfg.AdvertisePort,
		Addrs:        cfg.Advertise,
		Logger:       cfg.Logger,
		Events:       cfg.Events,
	})
	if err != nil {
		s.listener.Close()
		return fail(err)
	}

	return s, nil
}

// resolve fills in defaults and rejects what cannot work.
func (c *Config) resolve() error {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}

	c.StateDir = strings.TrimSpace(c.StateDir)
	if c.StateDir == "" {
		return errors.New("service: StateDir is required")
	}
	abs, err := filepath.Abs(c.StateDir)
	if err != nil {
		return fmt.Errorf("resolve state directory: %w", err)
	}
	c.StateDir = abs

	if strings.TrimSpace(c.Dest) == "" {
		c.Dest = filepath.Join(c.StateDir, "Photos")
	}
	if c.Dest, err = filepath.Abs(c.Dest); err != nil {
		return fmt.Errorf("resolve destination: %w", err)
	}

	if strings.TrimSpace(c.Name) == "" {
		c.Name = DefaultName()
	}
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = ":" + strconv.Itoa(discovery.DefaultTransferPort)
	}
	if c.DiscoveryPort == 0 {
		c.DiscoveryPort = discovery.DefaultPort
	}
	if c.DiscoveryPort < 0 || c.DiscoveryPort > 65535 {
		return fmt.Errorf("discovery port %d is not a valid port", c.DiscoveryPort)
	}
	if c.AdvertisePort < 0 || c.AdvertisePort > 65535 {
		return fmt.Errorf("advertise port %d is not a valid port", c.AdvertisePort)
	}
	return nil
}

// DefaultName is the receiver name a machine announces when nobody chose one.
func DefaultName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "Geda Receiver"
	}
	// macOS hostnames arrive as "studio-mac.local", which is not what anybody
	// calls their computer.
	return strings.TrimSuffix(host, ".local")
}

// Close releases everything Open acquired. Safe to call after Run returns.
func (s *Service) Close() error {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	return s.db.Close()
}

// Addr is the address transfers are actually accepted on.
func (s *Service) Addr() net.Addr { return s.listener.Addr() }

// Fingerprint is the identity a user compares across two screens.
func (s *Service) Fingerprint() string { return s.ident.Fingerprint() }

// Pin is the SPKI a paired device holds.
func (s *Service) Pin() string { return s.ident.Pin }

// DeviceID is what this receiver announces.
func (s *Service) DeviceID() string { return s.deviceID }

// Name is what this receiver calls itself.
func (s *Service) Name() string { return s.cfg.Name }

// Dest is the absolute destination directory.
//
// It is fixed for the life of a Service. Changing where files land means
// opening a new one -- the receiver hands its destination to tusd when it is
// built, and swapping it underneath an upload in flight would write half a
// file to each of two directories. A front end that offers the setting stops
// this service and opens another; the identity and the ledger are on disk, so
// nothing has to be re-paired.
func (s *Service) Dest() string { return s.files.Root() }

// StateDir is where the ledger and the identity live.
func (s *Service) StateDir() string { return s.cfg.StateDir }

// DB exposes the ledger for front ends that need to read it directly.
func (s *Service) DB() *store.DB { return s.db }

// Conversions is the queue of files waiting to be converted.
func (s *Service) Conversions() *formats.Queue { return s.conversions }

// Tools are the external converters found on this machine.
//
// They are what a settings screen reports, and what tells a user why nothing
// is being converted. Detected once at startup, so this is a snapshot.
func (s *Service) Tools() formats.Tools { return s.tools }

// OutputPolicy is what this receiver does with the files it stores.
func (s *Service) OutputPolicy(ctx context.Context) formats.Policy {
	return s.files.Policy(ctx)
}

// Run serves until ctx is cancelled.
//
// The receiver and the discovery responders run as one unit: if either fails
// the whole service stops, because a receiver that cannot be discovered is not
// doing its job and silently half-working is the worst outcome.
func (s *Service) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, ctx := errgroup.WithContext(ctx)

	// Everything that can fail before anything is started is built first: a
	// misconfigured responder must not leave a receiver already serving on a
	// goroutine nobody is waiting for.
	var responder *discovery.Responder
	if s.cfg.Discovery {
		var err error
		responder, err = discovery.NewResponder(discovery.ResponderConfig{
			DeviceID:     s.deviceID,
			Name:         s.cfg.Name,
			Platform:     runtime.GOOS,
			TransferPort: s.cfg.AdvertisePort,
			Port:         s.cfg.DiscoveryPort,
			SPKI:         s.ident.Pin,
			Paired:       func() bool { n, err := s.countDevices(ctx); return err == nil && n > 0 },
			Candidates:   s.candidates,
			Logger:       s.log,
		})
		if err != nil {
			return err
		}
	}

	g.Go(func() error {
		if err := s.srv.Serve(ctx, s.listener); err != nil {
			return fmt.Errorf("receiver: %w", err)
		}
		return nil
	})

	// Files queued for a phone are hashed here rather than by whoever queued
	// them, so that dropping a 2 GB archive onto the window does not freeze
	// it. It sweeps at startup too: a receiver stopped mid-hash comes back to
	// rows that nothing else would ever wake.
	g.Go(func() error {
		s.srv.Outbox().Run(ctx)
		return nil
	})

	// Conversions run behind the transfer, never in front of it: the bytes
	// are already stored and already acknowledged before anything here starts
	// (AGENTS.md §3.3). A machine with no ffmpeg receives exactly as well as
	// one with, and only converts less.
	g.Go(func() error {
		s.conversions.Run(ctx)
		return nil
	})

	if responder != nil {
		g.Go(func() error {
			if err := responder.Serve(ctx); err != nil {
				return fmt.Errorf("discovery: %w", err)
			}
			return nil
		})

		if s.cfg.MDNS {
			g.Go(func() error {
				// mDNS is the convenience layer, not the one that carries the
				// product: a box where 5353 is already taken by Avahi must
				// still receive files over the unicast layers.
				if err := responder.MDNS().Serve(ctx); err != nil {
					s.log.Warn("mDNS responder stopped; unicast discovery is unaffected", "error", err)
				}
				return nil
			})
		}
	}

	s.log.Info("receiver started",
		"version", s.cfg.Version,
		"device_id", s.deviceID,
		"name", s.cfg.Name,
		"listen", s.listener.Addr().String(),
		"dest", s.Dest(),
		"fingerprint", s.ident.Fingerprint())

	// Said once, at startup, and only when the configured policy actually
	// needs something that is not there. A receiver on the default preset
	// converts nothing and is told nothing about ffmpeg.
	if msg := s.tools.Explain(s.files.Policy(ctx)); msg != "" {
		s.log.Warn("output conversion is configured but cannot run", "detail", msg)
	}

	return g.Wait()
}

// candidates supplies the advertised address set.
//
// A configured list wins because the receiver cannot see the address peers
// reach it on: inside a bridged container the only local address is a private
// bridge address that nobody outside can dial.
func (s *Service) candidates() ([]string, error) {
	if len(s.cfg.Advertise) > 0 {
		return append([]string(nil), s.cfg.Advertise...), nil
	}
	return discovery.Candidates()
}

// CandidateAddrs is the candidate set as host:port, ready to be advertised.
func (s *Service) CandidateAddrs() ([]string, error) {
	hosts, err := s.candidates()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, net.JoinHostPort(h, strconv.Itoa(s.cfg.AdvertisePort)))
	}
	return out, nil
}

func (s *Service) countDevices(ctx context.Context) (int, error) {
	var n int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count devices: %w", err)
	}
	return n, nil
}

// deviceID reads the receiver's identifier, minting one on first run.
func deviceID(ctx context.Context, db *store.DB) (string, error) {
	id, err := db.Setting(ctx, SettingDeviceID)
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

	if err := db.SetSetting(ctx, SettingDeviceID, id); err != nil {
		return "", fmt.Errorf("save device id: %w", err)
	}
	return id, nil
}

// applyTemplate validates a naming template and stores it.
//
// Validation happens at startup rather than at the first upload: a template
// that cannot render is a configuration error, and finding out about it when
// the first photo of the holiday arrives is too late.
func applyTemplate(ctx context.Context, db *store.DB, tmpl string) error {
	if err := naming.Validate(tmpl); err != nil {
		return fmt.Errorf("naming template: %w", err)
	}
	return db.SetSetting(ctx, storage.SettingTemplate, tmpl)
}

// applyPolicy validates an output policy and stores it.
//
// Validated at startup for the same reason the naming template is: a policy
// that cannot be applied is a configuration error, and finding out about it
// when the first photo of the holiday arrives is too late.
func applyPolicy(ctx context.Context, db *store.DB, p formats.Policy) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("output policy: %w", err)
	}
	preset, matrix, err := p.Marshal()
	if err != nil {
		return fmt.Errorf("output policy: %w", err)
	}
	if err := db.SetSetting(ctx, formats.SettingPreset, preset); err != nil {
		return fmt.Errorf("save the output preset: %w", err)
	}
	if err := db.SetSetting(ctx, formats.SettingMatrix, matrix); err != nil {
		return fmt.Errorf("save the output matrix: %w", err)
	}
	return nil
}

// nullTime parses a nullable RFC3339 column.
func nullTime(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil
	}
	return &t
}
