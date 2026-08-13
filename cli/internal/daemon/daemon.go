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

// Package daemon runs core's receiver as a long-lived headless process.
//
// The receiver itself is core/service, which the desktop app runs too. What
// this package adds is the part that only a machine with no screen needs: the
// configuration file translated into a service config, and the control socket
// that `gedad pair` talks to (AGENTS.md §2).
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/geda/geda-transfer/core/service"

	"github.com/geda/geda-transfer/cli/internal/config"
	"github.com/geda/geda-transfer/cli/internal/control"
)

// Daemon is a running headless receiver.
type Daemon struct {
	*service.Service

	cfg config.Config
	log *slog.Logger
}

// New opens the state directory and prepares every component.
func New(ctx context.Context, cfg config.Config, version string, log *slog.Logger) (*Daemon, error) {
	svc, err := service.Open(ctx, service.Config{
		Name:           cfg.Name,
		Version:        version,
		StateDir:       cfg.StateDir,
		Dest:           cfg.Dest,
		Listen:         cfg.Listen,
		DiscoveryPort:  cfg.DiscoveryPort,
		AdvertisePort:  cfg.AdvertisePort,
		Advertise:      cfg.Advertise,
		MDNS:           cfg.MDNS,
		Discovery:      cfg.Discovery,
		NamingTemplate: cfg.NamingTemplate,
		Logger:         log,
	})
	if err != nil {
		return nil, err
	}
	return &Daemon{Service: svc, cfg: cfg, log: log}, nil
}

// Run serves until ctx is cancelled.
//
// The receiver and the control socket run as one unit: a daemon that cannot be
// paired with is not doing its job, and silently half-working is the worst
// outcome for something with no UI.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error { return d.Service.Run(ctx) })

	g.Go(func() error {
		if err := control.Serve(ctx, d.cfg.ControlSocket, d); err != nil {
			return fmt.Errorf("control socket: %w", err)
		}
		return nil
	})

	d.log.Info("gedad started", "socket", d.cfg.ControlSocket)
	return g.Wait()
}

// Addr is the address transfers are actually accepted on.
func (d *Daemon) Addr() net.Addr { return d.Service.Addr() }

// The control socket's Backend is satisfied by the embedded service, whose
// Status, Pair, Devices, and Unpair are the same operations the desktop's
// menus call. These wrappers exist only to pin the types down at compile time.
var _ control.Backend = (*Daemon)(nil)

// Status implements control.Backend.
func (d *Daemon) Status(ctx context.Context) (control.Status, error) {
	return d.Service.Status(ctx)
}

// Pair implements control.Backend.
func (d *Daemon) Pair(ctx context.Context, ttl time.Duration) (control.Offer, error) {
	return d.Service.Pair(ctx, ttl)
}

// Devices implements control.Backend.
func (d *Daemon) Devices(ctx context.Context) ([]control.Device, error) {
	return d.Service.Devices(ctx)
}
