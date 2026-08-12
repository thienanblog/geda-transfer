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

// Command gatepeer verifies the P2 gate: two peers on different subnets find
// each other within three seconds, in both directions.
//
// It is a test harness, not the product. Each instance runs a full receiver --
// discovery responder, TLS server, real certificate -- then discovers the peer
// it was told to expect and connects to it over pinned TLS. Discovery that
// produces an address nobody can connect to would pass a weaker check and fail
// a user, so the connection is part of the test.
//
// See scripts/verify-p2.sh.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/geda/geda-transfer/core/client"
	"github.com/geda/geda-transfer/core/discovery"
	"github.com/geda/geda-transfer/core/identity"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gatepeer:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dir        = flag.String("dir", "", "state directory (required)")
		id         = flag.String("id", "", "this peer's device id (required)")
		name       = flag.String("name", "", "this peer's display name")
		listen     = flag.String("listen", ":47891", "TLS listen address")
		udpPort    = flag.Int("udp-port", discovery.DefaultPort, "discovery port")
		sweep      = flag.String("sweep", "", "comma-separated CIDRs to sweep (L3)")
		expect     = flag.String("expect", "", "device id of the peer to find (required)")
		scan       = flag.Duration("scan", 3*time.Second, "budget for one scan -- the gate")
		wait       = flag.Duration("wait", 90*time.Second, "how long to keep retrying while the peer starts")
		unicast    = flag.Bool("unicast-only", true, "disable mDNS and broadcast, leaving only the unicast layers")
		serveAfter = flag.Duration("serve-after", 5*time.Second, "keep answering after success, so the peer can finish too")
	)
	flag.Parse()

	switch {
	case *dir == "":
		return fmt.Errorf("-dir is required")
	case *id == "":
		return fmt.Errorf("-id is required")
	case *expect == "":
		return fmt.Errorf("-expect is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	db, err := store.Open(ctx, filepath.Join(*dir, "ledger.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	files, err := storage.New(db, filepath.Join(*dir, "Photos"))
	if err != nil {
		return err
	}

	ident, err := identity.Load(filepath.Join(*dir, "identity"))
	if err != nil {
		return err
	}

	srv, err := receiver.New(receiver.Config{
		DeviceID: *id,
		Name:     *name,
		DB:       db,
		Files:    files,
		Identity: ident,
		Logger:   log,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	go func() {
		if err := srv.Serve(ctx, ln); err != nil {
			log.Error("receiver stopped", "error", err)
		}
	}()

	transferPort := ln.Addr().(*net.TCPAddr).Port
	responder, err := discovery.NewResponder(discovery.ResponderConfig{
		DeviceID:     *id,
		Name:         *name,
		Platform:     "linux",
		TransferPort: transferPort,
		Port:         *udpPort,
		SPKI:         ident.Pin,
		Logger:       log,
	})
	if err != nil {
		return err
	}
	go func() {
		if err := responder.Serve(ctx); err != nil {
			log.Error("responder stopped", "error", err)
		}
	}()
	if !*unicast {
		go func() {
			if err := responder.MDNS().Serve(ctx); err != nil {
				log.Error("mdns responder stopped", "error", err)
			}
		}()
	}

	subnets, err := parseSubnets(*sweep)
	if err != nil {
		return err
	}

	found, elapsed, err := findPeer(ctx, findConfig{
		port:    *udpPort,
		subnets: subnets,
		expect:  *expect,
		scan:    *scan,
		wait:    *wait,
		unicast: *unicast,
		log:     log,
	})
	if err != nil {
		return err
	}

	addr, err := reach(ctx, found)
	if err != nil {
		return fmt.Errorf("discovered %s but could not connect to it: %w", *expect, err)
	}

	report := map[string]any{
		"peer":       found.DeviceID,
		"name":       found.Name,
		"elapsed_ms": elapsed.Milliseconds(),
		"sources":    found.Sources,
		"from":       found.From.String(),
		"connected":  addr,
		"candidates": found.Addrs,
	}
	out, err := json.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Println(string(out))

	// The peer is very likely still scanning for us. Exiting the instant we
	// succeed would take this responder off the network and fail the other
	// direction, which is half the gate.
	select {
	case <-ctx.Done():
	case <-time.After(*serveAfter):
	}
	return nil
}

type findConfig struct {
	port    int
	subnets []netip.Prefix
	expect  string
	scan    time.Duration
	wait    time.Duration
	unicast bool
	log     *slog.Logger
}

// findPeer scans until the expected peer answers.
//
// Retrying is about the two containers not starting at the same instant. The
// figure the gate is measured against is the duration of the scan that
// succeeded, which is bounded by -scan.
func findPeer(ctx context.Context, cfg findConfig) (discovery.Result, time.Duration, error) {
	deadline := time.Now().Add(cfg.wait)

	for attempt := 1; ; attempt++ {
		start := time.Now()
		result, err := discovery.First(ctx, discovery.Config{
			Port:             cfg.port,
			Subnets:          cfg.subnets,
			Timeout:          cfg.scan,
			DisableMDNS:      cfg.unicast,
			DisableBroadcast: cfg.unicast,
			Logger:           cfg.log,
		}, func(r discovery.Result) bool { return r.DeviceID == cfg.expect })

		if err == nil {
			return result, time.Since(start), nil
		}
		if ctx.Err() != nil {
			return discovery.Result{}, 0, ctx.Err()
		}
		if time.Now().After(deadline) {
			return discovery.Result{}, 0, fmt.Errorf("%s did not answer within %s (%d scans)", cfg.expect, cfg.wait, attempt)
		}

		fmt.Fprintf(os.Stderr, "gatepeer: scan %d found no %s, retrying\n", attempt, cfg.expect)
	}
}

// reach connects to the discovered peer over pinned TLS.
//
// The pin comes from the announce rather than from a QR code, which is weaker
// than real pairing and exactly right here: what is under test is that the
// candidate set discovery produced contains an address that actually works
// from this subnet.
func reach(ctx context.Context, found discovery.Result) (string, error) {
	c, err := client.New(client.Config{
		Pin:            found.SPKI,
		Addrs:          found.TransferAddrs(),
		DialTimeout:    3 * time.Second,
		RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		return "", err
	}

	info, err := c.Info(ctx)
	if err != nil {
		return "", err
	}
	if info.DeviceID != found.DeviceID {
		return "", fmt.Errorf("connected to %s, expected %s", info.DeviceID, found.DeviceID)
	}
	return c.Addrs()[0], nil
}

func parseSubnets(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("bad -sweep entry %q: %w", part, err)
		}
		out = append(out, prefix)
	}
	return out, nil
}
