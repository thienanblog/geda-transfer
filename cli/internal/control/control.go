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

// Package control is the local admin channel of the daemon.
//
// A headless receiver still has to be paired, and pairing offers live only in
// the memory of the running process (docs/DECISIONS.md). So `gedad pair` is
// not a second program that reads the ledger -- it is a request to the running
// daemon, over a Unix socket in the state directory.
//
// The socket is the authorisation boundary: it is created 0600 and owned by
// the user the daemon runs as, so reaching it means being that user or root on
// that machine. Nothing here is exposed on the network, and the daemon's own
// TLS port carries no admin endpoints at all -- issuing a pairing offer to
// anyone who can reach the receiver would defeat the point of a QR code that
// requires physical presence.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/geda/geda-transfer/core/service"
)

// The socket reports exactly what core reports. These are aliases rather than
// copies so that a field added to a status in core cannot be silently missing
// from `gedad status -json`, which is what somebody's monitoring script reads.
type (
	// Status is what `gedad status` reports.
	Status = service.Status

	// Offer is a pairing invitation, as `gedad pair` renders it.
	Offer = service.Offer

	// Device is one paired device.
	Device = service.Device
)

// Backend is the daemon, as the control socket sees it.
type Backend interface {
	Status(ctx context.Context) (Status, error)
	Pair(ctx context.Context, ttl time.Duration) (Offer, error)
	Devices(ctx context.Context) ([]Device, error)
	Unpair(ctx context.Context, deviceID string) error
}

// ErrNotRunning reports that no daemon is listening on the socket.
var ErrNotRunning = errors.New("no gedad daemon is listening")

type pairRequest struct {
	TTLSeconds int `json:"ttl_seconds"`
}

type unpairRequest struct {
	DeviceID string `json:"device_id"`
}

type errorBody struct {
	Error string `json:"error"`
}

// Handler routes the control endpoints.
func Handler(b Backend) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		status, err := b.Status(r.Context())
		respond(w, status, err)
	})

	mux.HandleFunc("POST /v1/pair", func(w http.ResponseWriter, r *http.Request) {
		var req pairRequest
		if !decode(w, r, &req) {
			return
		}
		offer, err := b.Pair(r.Context(), time.Duration(req.TTLSeconds)*time.Second)
		respond(w, offer, err)
	})

	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		devices, err := b.Devices(r.Context())
		if devices == nil {
			devices = []Device{}
		}
		respond(w, devices, err)
	})

	mux.HandleFunc("POST /v1/unpair", func(w http.ResponseWriter, r *http.Request) {
		var req unpairRequest
		if !decode(w, r, &req) {
			return
		}
		if req.DeviceID == "" {
			writeError(w, http.StatusBadRequest, "device_id is required")
			return
		}
		respond(w, struct{}{}, b.Unpair(r.Context(), req.DeviceID))
	})

	return mux
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.ContentLength == 0 {
		return true
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "body must be JSON")
		return false
	}
	return true
}

func respond(w http.ResponseWriter, body any, err error) {
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: msg})
}

// Serve runs the control socket at path until ctx is done.
func Serve(ctx context.Context, path string, b Backend) error {
	ln, err := Listen(path)
	if err != nil {
		return err
	}
	defer ln.Close()

	srv := &http.Server{
		Handler:           Handler(b),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	err = srv.Serve(ln)
	<-done
	// Removing the socket on the way out keeps the next start from having to
	// decide whether a leftover file means a running daemon.
	_ = os.Remove(path)

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Listen binds the control socket, clearing a stale one left by a crash.
//
// "Stale" is decided by dialling, never by age: a socket file that still has
// a daemon behind it must stop this one from starting, because two daemons
// sharing a state directory would both answer pairing requests.
func Listen(path string) (net.Listener, error) {
	if err := checkSocketPath(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("prepare control socket directory: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		conn, derr := net.DialTimeout("unix", path, time.Second)
		if derr == nil {
			conn.Close()
			return nil, fmt.Errorf("another gedad is already running on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale control socket: %w", err)
		}
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on control socket: %w", err)
	}
	// The socket is the admin boundary; group and world have no business
	// issuing pairing offers.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("secure control socket: %w", err)
	}
	return ln, nil
}
