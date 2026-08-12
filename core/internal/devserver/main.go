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

// Command devserver runs a receiver with one pre-paired device so that phase
// gates can be checked by hand with curl.
//
// It is not the product. The real daemon is cli/gedad, built in P3; pairing is
// built in P2. Until then this exists so that "curl can upload, resume, and be
// skipped on a repeat" is verified against a real server over real TLS rather
// than only in Go tests.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/geda/geda-transfer/core/identity"
	"github.com/geda/geda-transfer/core/receiver"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devserver:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dir   = flag.String("dir", "", "state directory (required)")
		addr  = flag.String("addr", "127.0.0.1:47891", "listen address")
		token = flag.String("token", "dev-token", "token for the pre-paired device")
	)
	flag.Parse()

	if *dir == "" {
		return fmt.Errorf("-dir is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, filepath.Join(*dir, "ledger.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at)
		VALUES ('dev-1', 'Test iPhone', 'ios', 'unset', ?, ?)
		ON CONFLICT(id) DO UPDATE SET token_hash = excluded.token_hash`,
		receiver.HashToken(*token), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("pre-pair test device: %w", err)
	}

	files, err := storage.New(db, filepath.Join(*dir, "Photos"))
	if err != nil {
		return err
	}

	id, err := identity.Load(filepath.Join(*dir, "identity"))
	if err != nil {
		return err
	}

	srv, err := receiver.New(receiver.Config{
		DeviceID: "receiver-dev",
		Name:     "Devserver",
		DB:       db,
		Files:    files,
		Identity: id,
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}

	fmt.Printf("listening on https://%s\n", ln.Addr())
	fmt.Printf("pin         %s\n", id.Pin)
	fmt.Printf("fingerprint %s\n", id.Fingerprint())
	fmt.Printf("destination %s\n", files.Root())

	return srv.Serve(ctx, ln)
}
