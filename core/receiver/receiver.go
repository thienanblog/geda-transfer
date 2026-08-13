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

// Package receiver serves the Geda Transfer protocol over HTTP/2 and TLS 1.3.
//
// HTTP is not an implementation detail that could be swapped later. iOS can
// only continue a transfer while the app is suspended through a background
// URLSession, and that API speaks HTTP and nothing else (AGENTS.md §3.1).
package receiver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tus "github.com/tus/tusd/v2/pkg/handler"

	"github.com/geda/geda-transfer/core/events"
	"github.com/geda/geda-transfer/core/identity"
	"github.com/geda/geda-transfer/core/outbox"
	"github.com/geda/geda-transfer/core/pairing"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"
)

// ProtocolVersion is the wire version this receiver speaks.
const ProtocolVersion = 1

// UploadPath is where tus uploads are created.
const UploadPath = "/v1/files/"

// incomingMaxAge is how long a partial upload survives without progress. Long
// enough for a phone to be away for a weekend, short enough that abandoned
// uploads cannot fill a NAS.
const incomingMaxAge = 7 * 24 * time.Hour

// Config describes a receiver.
type Config struct {
	// DeviceID identifies this receiver to peers.
	DeviceID string

	// Name is what users see when choosing a destination.
	Name string

	// DB is the ledger.
	DB *store.DB

	// Files places received files.
	Files *storage.Store

	// Identity supplies the TLS certificate and the pinned public key.
	Identity *identity.Identity

	// TransferPort is the TCP port clients reach this receiver on. It is
	// advertised in pairing offers, so it must be the port users can actually
	// dial rather than whatever ephemeral port a test listener took. Defaults
	// to discovery.DefaultTransferPort.
	TransferPort int

	// Addrs overrides the advertised candidate set. Empty means "every local
	// interface address", which is what production wants; tests and unusual
	// deployments (a container behind a published port) set it explicitly.
	Addrs []string

	// Logger receives operational messages. Defaults to slog.Default().
	Logger *slog.Logger

	// Events, when set, receives the lifecycle of every upload. It is what a
	// desktop window showing a transfer in progress subscribes to. Publishing
	// never blocks, so a UI that stops reading cannot slow a transfer down.
	Events *events.Bus
}

// Server is a running receiver.
type Server struct {
	cfg     Config
	log     *slog.Logger
	uploads *uploadStore
	tus     *tus.Handler
	mux     *http.ServeMux
	http    *http.Server

	offers *pairing.Offers

	// outbox is what this receiver is holding for phones to collect. It lives
	// here rather than in the front end because there must be exactly one of
	// it: queueing a file has to wake the same worker that the HTTP handlers
	// read from.
	outbox *outbox.Queue

	seenMu   sync.Mutex
	lastSeen map[string]time.Time
}

// New builds a receiver. Call Serve to start it.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.DB == nil:
		return nil, errors.New("receiver: DB is required")
	case cfg.Files == nil:
		return nil, errors.New("receiver: Files is required")
	case cfg.Identity == nil:
		return nil, errors.New("receiver: Identity is required")
	case cfg.DeviceID == "":
		return nil, errors.New("receiver: DeviceID is required")
	}

	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	s := &Server{
		cfg:      cfg,
		log:      log,
		uploads:  newUploadStore(cfg.Files, cfg.Events),
		offers:   pairing.NewOffers(nil),
		outbox:   outbox.New(cfg.DB, log),
		lastSeen: make(map[string]time.Time),
	}

	composer := tus.NewStoreComposer()
	composer.UseCore(s.uploads)
	composer.UseTerminater(s.uploads)

	handler, err := tus.NewHandler(tus.Config{
		BasePath:      UploadPath,
		StoreComposer: composer,
		// Config.Logger is left unset on purpose. tusd v2.10 still types it as
		// golang.org/x/exp/slog.Logger, which is a different type from the
		// standard library's log/slog.Logger and cannot be converted. Taking
		// x/exp as a direct dependency to satisfy one field is a worse trade
		// than letting tusd log through its own default.
		// The client is told the final path once the file is committed, so it
		// knows the transfer is durable and not merely uploaded.
		PreFinishResponseCallback: s.onFinish,
		// Metadata that decides who a file belongs to is taken from the
		// authenticated session, never from what the client claims.
		PreUploadCreateCallback: s.onCreate,
		DisableDownload:         true,
	})
	if err != nil {
		return nil, fmt.Errorf("receiver: build upload handler: %w", err)
	}
	s.tus = handler

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /v1/info", s.handleInfo)
	// Pairing carries its own authorisation -- the single-use PSK from the QR
	// code -- because a device that has no token yet is the entire point.
	s.mux.HandleFunc("POST /v1/pair", s.handlePair)
	s.mux.Handle("POST /v1/have", s.authenticated(http.HandlerFunc(s.handleHave)))
	s.mux.Handle(UploadPath, s.authenticated(http.StripPrefix(strings.TrimSuffix(UploadPath, "/"), handler)))

	// Desktop to mobile. The phone pulls, because nothing can push to a
	// suspended iOS app (AGENTS.md §3.7). Every one of these is scoped to the
	// authenticated device inside the queue, so one phone cannot see, fetch,
	// or acknowledge another's files.
	s.mux.Handle("GET "+OutboxPath, s.authenticated(http.HandlerFunc(s.handleOutboxList)))
	s.mux.Handle("GET "+OutboxPath+"/{id}", s.authenticated(http.HandlerFunc(s.handleOutboxFetch)))
	s.mux.Handle("HEAD "+OutboxPath+"/{id}", s.authenticated(http.HandlerFunc(s.handleOutboxFetch)))
	s.mux.Handle("DELETE "+OutboxPath+"/{id}", s.authenticated(http.HandlerFunc(s.handleOutboxAck)))

	return s, nil
}

// Handler exposes the routes, for tests and for embedding.
func (s *Server) Handler() http.Handler { return s.mux }

// Serve accepts connections on ln until the context is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.http = &http.Server{
		Handler:           s.mux,
		TLSConfig:         s.cfg.Identity.TLSConfig(),
		ReadHeaderTimeout: 30 * time.Second,
		// No write timeout: a single upload of a 4K video legitimately holds
		// one request open for a long time.
		IdleTimeout: 2 * time.Minute,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	// A receiver on a NAS runs for months. Sweeping only at startup would let
	// abandoned partial uploads accumulate until the volume fills, which turns
	// a working backup into a silent failure.
	go s.sweepPeriodically(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdownCtx)
	}()

	err := s.http.ServeTLS(ln, "", "")
	<-done

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// sweepInterval is how often abandoned uploads are cleared out.
const sweepInterval = 6 * time.Hour

func (s *Server) sweepPeriodically(ctx context.Context) {
	sweep := func() {
		if err := s.uploads.sweepIncoming(incomingMaxAge); err != nil {
			s.log.Warn("could not sweep abandoned uploads", "error", err)
		}
	}
	sweep()

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// deviceKey carries the authenticated device through a request.
type deviceKey struct{}

type authDevice struct {
	ID   string
	Name string
}

// DeviceFrom returns the authenticated device on a request context.
func DeviceFrom(ctx context.Context) (id, name string, ok bool) {
	d, ok := ctx.Value(deviceKey{}).(authDevice)
	return d.ID, d.Name, ok
}

// authenticated rejects requests without a valid device token.
func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a device token is required", false)
			return
		}

		device, err := s.lookupDevice(r.Context(), token)
		if errors.Is(err, errUnknownToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "unknown or revoked device token", false)
			return
		}
		if err != nil {
			s.log.Error("device lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not verify the device", true)
			return
		}

		ctx := context.WithValue(r.Context(), deviceKey{}, device)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

var errUnknownToken = errors.New("unknown token")

// HashToken derives the value stored in the ledger. The token itself is never
// written down, so a leaked database does not hand over working credentials.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Server) lookupDevice(ctx context.Context, token string) (authDevice, error) {
	var d authDevice
	err := s.cfg.DB.SQL().QueryRowContext(ctx, `
		SELECT id, name FROM devices
		WHERE token_hash = ? AND revoked_at IS NULL`,
		HashToken(token)).Scan(&d.ID, &d.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return authDevice{}, errUnknownToken
	}
	if err != nil {
		return authDevice{}, err
	}

	s.touch(ctx, d.ID)
	return d, nil
}

// lastSeenInterval is how coarse the last_seen_at column is allowed to be.
//
// Transferring ten thousand photos is tens of thousands of authenticated
// requests. Writing the ledger on every one of them turns a display-only
// timestamp into the busiest write in the system, competing with the rows that
// actually matter.
const lastSeenInterval = time.Minute

func (s *Server) touch(ctx context.Context, deviceID string) {
	now := time.Now()

	s.seenMu.Lock()
	if last, ok := s.lastSeen[deviceID]; ok && now.Sub(last) < lastSeenInterval {
		s.seenMu.Unlock()
		return
	}
	s.lastSeen[deviceID] = now
	s.seenMu.Unlock()

	// Best effort; a busy ledger must not fail an otherwise valid request.
	_, _ = s.cfg.DB.SQL().ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ? WHERE id = ?`,
		now.UTC().Format(time.RFC3339Nano), deviceID)
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

// ConstantTimeEqual compares two tokens without leaking their contents through
// timing. Exported because pairing in a later phase needs the same guarantee.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// onCreate stamps the authenticated device onto a new upload.
//
// Without this a device could set device_id to a peer's identifier and file
// its uploads under someone else's name.
func (s *Server) onCreate(hook tus.HookEvent) (tus.HTTPResponse, tus.FileInfoChanges, error) {
	id, name, ok := DeviceFrom(hook.Context)
	if !ok {
		return tus.HTTPResponse{}, tus.FileInfoChanges{},
			tus.NewError("ERR_UNAUTHORIZED", "a device token is required", http.StatusUnauthorized)
	}

	md := make(tus.MetaData, len(hook.Upload.MetaData)+2)
	for k, v := range hook.Upload.MetaData {
		md[k] = v
	}
	md["device_id"] = id
	md["device_name"] = name

	return tus.HTTPResponse{}, tus.FileInfoChanges{MetaData: md}, nil
}

// onFinish reports where the file was stored, and whether it was already held.
func (s *Server) onFinish(hook tus.HookEvent) (tus.HTTPResponse, error) {
	headers := tus.HTTPHeader{}
	if path := hook.Upload.MetaData["stored_path"]; path != "" {
		// Base64 keeps a UTF-8 path safe in a header, which is Latin-1 by
		// specification.
		headers["Geda-Stored-Path"] = base64.StdEncoding.EncodeToString([]byte(path))
	}
	if hook.Upload.MetaData["deduplicated"] == "1" {
		headers["Geda-Deduplicated"] = "1"
	}
	return tus.HTTPResponse{Header: headers}, nil
}

// infoResponse is the unauthenticated capability document.
type infoResponse struct {
	Versions []int  `json:"versions"`
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Pin      string `json:"spki"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	// Deliberately unauthenticated: a client needs this to decide whether it
	// is talking to the receiver it paired with, before it has a session.
	writeJSON(w, http.StatusOK, infoResponse{
		Versions: []int{ProtocolVersion},
		DeviceID: s.cfg.DeviceID,
		Name:     s.cfg.Name,
		Pin:      s.cfg.Identity.Pin,
	})
}

// haveRequest is the dedup probe (docs/PROTOCOL.md §4).
type haveRequest struct {
	Items []haveItem `json:"items"`
}

type haveItem struct {
	ID         string `json:"id"`
	Size       int64  `json:"size"`
	CapturedAt string `json:"captured_at"`
	HeadHash   string `json:"head_hash"`
}

type haveResponse struct {
	Results []haveResult `json:"results"`
}

type haveResult struct {
	ID   string `json:"id"`
	Have bool   `json:"have"`
	Path string `json:"path,omitempty"`
}

// maxHaveItems bounds one probe. Batching is the point of this endpoint, but
// an unbounded batch is an easy way to make the receiver do unbounded work.
const maxHaveItems = 1000

func (s *Server) handleHave(w http.ResponseWriter, r *http.Request) {
	deviceID, _, _ := DeviceFrom(r.Context())

	var req haveRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be JSON", false)
		return
	}
	if len(req.Items) > maxHaveItems {
		writeError(w, http.StatusBadRequest, "too_many_items",
			fmt.Sprintf("at most %d items per probe", maxHaveItems), false)
		return
	}

	results := make([]haveResult, 0, len(req.Items))
	for _, item := range req.Items {
		path, err := s.lookupHave(r.Context(), deviceID, item)
		if err != nil {
			s.log.Error("dedup probe failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "probe failed", true)
			return
		}
		results = append(results, haveResult{ID: item.ID, Have: path != "", Path: path})
	}

	writeJSON(w, http.StatusOK, haveResponse{Results: results})
}

// lookupHave answers whether this device already has a matching file.
//
// The match is on size, capture date, and the head hash. That is a filter, not
// proof: the full hash computed during upload remains the authority, and is
// what a client must rely on before deleting anything.
func (s *Server) lookupHave(ctx context.Context, deviceID string, item haveItem) (string, error) {
	if item.HeadHash == "" || item.Size <= 0 {
		return "", nil
	}

	var (
		path string
		err  error
	)
	if item.CapturedAt == "" {
		err = s.cfg.DB.SQL().QueryRowContext(ctx, `
			SELECT stored_path FROM files
			WHERE device_id = ? AND size = ? AND head_hash = ? AND captured_at IS NULL
			LIMIT 1`,
			deviceID, item.Size, item.HeadHash).Scan(&path)
	} else {
		t, perr := time.Parse(time.RFC3339, item.CapturedAt)
		if perr != nil {
			return "", nil
		}
		err = s.cfg.DB.SQL().QueryRowContext(ctx, `
			SELECT stored_path FROM files
			WHERE device_id = ? AND size = ? AND head_hash = ? AND captured_at = ?
			LIMIT 1`,
			deviceID, item.Size, item.HeadHash, t.UTC().Format(time.RFC3339Nano)).Scan(&path)
	}

	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

// errorBody is the shape every error uses (docs/PROTOCOL.md §7).
type errorBody struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func writeError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	writeJSON(w, status, errorBody{Error: code, Message: message, Retryable: retryable})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// joinHostPort formats an address for dialling, bracketing IPv6.
func joinHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// forgetDevice drops cached state for a device that is no longer paired.
func (s *Server) forgetDevice(deviceID string) {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	delete(s.lastSeen, deviceID)
}
