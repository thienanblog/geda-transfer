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

// Package outbox holds files a receiver is offering to a paired phone.
//
// The direction is the awkward one. A desktop cannot push to a locked or
// suspended iPhone -- there is no APNs, no push server, and nothing in this
// product that would justify one (AGENTS.md §3.7). So the desktop does not
// send; it offers. Queueing a file writes a row here, and the phone collects
// it the next time the user opens the app, continuing in a background
// URLSession after they put the phone down (docs/PROTOCOL.md §6).
//
// Three properties shape the design:
//
//   - The bytes stay where they are. A row points at the file on disk rather
//     than copying it into a spool, because queueing a 2 GB archive must not
//     cost 2 GB of disk. The size and mtime seen at hashing time are recorded
//     and re-checked before the file is served, so a file edited in the
//     meantime fails its item instead of contradicting its digest.
//   - Hashing happens off the caller's goroutine. A window that freezes for
//     thirty seconds because somebody dragged in a video is a broken window,
//     so Add returns as soon as the rows exist and a worker fills in the
//     digests. Only hashed items are offered to the phone.
//   - Nothing here trusts the network. Item ids arrive over HTTP; paths never
//     do. Every lookup is scoped to the authenticated device, so one phone
//     cannot see, fetch, or acknowledge another phone's items.
package outbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geda/geda-transfer/core/store"
)

// ErrNotFound reports that no item matches, either because it never existed or
// because it belongs to another device. The two are deliberately the same
// answer: a phone must not be able to probe for the existence of items that
// are not its own.
var ErrNotFound = errors.New("outbox: no such item")

// ErrNotReady reports that an item exists but has not been hashed yet.
var ErrNotReady = errors.New("outbox: item is not ready")

// ErrSourceChanged reports that the file on disk is no longer the one that was
// hashed. Serving it anyway would hand the phone bytes that do not match the
// digest it was told to verify, which it would correctly reject after
// downloading all of them.
var ErrSourceChanged = errors.New("outbox: the file changed after it was queued")

// State is where an item is in its life.
type State string

const (
	// StatePending is queued but not yet hashed. Not offered to the phone.
	StatePending State = "pending"

	// StateReady is hashed and on offer.
	StateReady State = "ready"

	// StateClaimed means the phone has been handed the bytes at least once.
	// It is not a promise that they arrived: a background download can be
	// interrupted and restarted, and the item stays claimed throughout.
	StateClaimed State = "claimed"

	// StateDelivered means the phone recomputed the digest, matched it, and
	// said so. This is the only state in which the file is known to have
	// arrived intact.
	StateDelivered State = "delivered"

	// StateFailed means the source is gone, unreadable, or was modified after
	// queueing.
	StateFailed State = "failed"
)

// Item is one file on offer to one device.
type Item struct {
	ID       string `json:"id"`
	DeviceID string `json:"device_id"`

	// Filename is the source's basename. It is what the phone is offered, and
	// on that side it is untrusted input: a name is not a path until it has
	// been sanitised.
	Filename string `json:"filename"`

	// SourcePath is where the file lives on this machine. It is never sent to
	// the phone -- the phone is given an opaque item id and nothing else.
	SourcePath string `json:"source_path"`

	Size int64 `json:"size"`

	// SHA256 is the hex digest the phone verifies against. Empty until the
	// item has been hashed.
	SHA256 string `json:"sha256"`

	// Kind decides where the file lands on the phone: photo and video may go
	// to the Photo Library, file never does.
	Kind string `json:"kind"`

	CapturedAt  *time.Time `json:"captured_at,omitempty"`
	State       State      `json:"state"`
	Error       string     `json:"error,omitempty"`
	QueuedAt    time.Time  `json:"queued_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`

	// sourceMtime is the modification time seen when the file was hashed,
	// re-checked before the file is served. Unexported because it is
	// bookkeeping rather than something a front end has any use for.
	sourceMtime string
}

// Ready reports whether the phone may be offered this item.
func (i Item) Ready() bool {
	return (i.State == StateReady || i.State == StateClaimed) && i.SHA256 != ""
}

// Queue is a receiver's outbox, backed by the ledger.
type Queue struct {
	db  *store.DB
	log *slog.Logger

	// wake nudges the worker after Add. It is buffered to one and written to
	// without blocking, so a caller queueing a thousand files does not wait
	// on the worker, and a worker already awake does not need telling twice.
	wake chan struct{}
}

// New builds a queue over the ledger. Call Run to start hashing.
func New(db *store.DB, log *slog.Logger) *Queue {
	if log == nil {
		log = slog.Default()
	}
	return &Queue{db: db, log: log, wake: make(chan struct{}, 1)}
}

// Add queues files for a device.
//
// Paths are resolved and checked here so that the user learns about a
// directory or a missing file while the file picker is still in mind, rather
// than through an item that fails silently later. Everything expensive --
// reading the file, hashing it -- happens on the worker.
func (q *Queue) Add(ctx context.Context, deviceID string, paths []string) ([]Item, error) {
	if deviceID == "" {
		return nil, errors.New("outbox: a device is required")
	}
	if err := q.requirePairedDevice(ctx, deviceID); err != nil {
		return nil, err
	}

	items := make([]Item, 0, len(paths))
	for _, path := range paths {
		item, err := q.add(ctx, deviceID, path)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	q.notify()
	return items, nil
}

func (q *Queue) add(ctx context.Context, deviceID, path string) (Item, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Item{}, fmt.Errorf("outbox: resolve %s: %w", path, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return Item{}, fmt.Errorf("outbox: %w", err)
	}
	if info.IsDir() {
		return Item{}, fmt.Errorf("outbox: %s is a folder; queue the files inside it", abs)
	}
	if !info.Mode().IsRegular() {
		return Item{}, fmt.Errorf("outbox: %s is not a regular file", abs)
	}

	id, err := newID()
	if err != nil {
		return Item{}, err
	}

	item := Item{
		ID:         id,
		DeviceID:   deviceID,
		Filename:   filepath.Base(abs),
		SourcePath: abs,
		Size:       info.Size(),
		Kind:       Classify(abs),
		State:      StatePending,
		QueuedAt:   time.Now().UTC(),
	}

	// A file's mtime is the only capture date available without parsing EXIF,
	// and it is a great deal better than nothing: without it the Photo Library
	// files everything under the day it was downloaded.
	if item.Kind != KindFile {
		captured := info.ModTime().UTC()
		item.CapturedAt = &captured
	}

	_, err = q.db.SQL().ExecContext(ctx, `
		INSERT INTO outbox (id, device_id, source_path, filename, size, kind,
		                    captured_at, state, queued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.DeviceID, item.SourcePath, item.Filename, item.Size, item.Kind,
		nullString(formatTimePtr(item.CapturedAt)), string(item.State), format(item.QueuedAt))
	if err != nil {
		return Item{}, fmt.Errorf("outbox: queue %s: %w", abs, err)
	}
	return item, nil
}

// requirePairedDevice refuses to queue for a device that cannot collect.
//
// The foreign key would catch a device that never existed, but not one that
// was unpaired: its row survives with revoked_at set so that history is not
// lost, and queueing for it would produce items nothing will ever come for.
func (q *Queue) requirePairedDevice(ctx context.Context, deviceID string) error {
	var revoked sql.NullString
	err := q.db.SQL().QueryRowContext(ctx,
		`SELECT revoked_at FROM devices WHERE id = ?`, deviceID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("outbox: %s is not a paired device", deviceID)
	}
	if err != nil {
		return fmt.Errorf("outbox: look up device: %w", err)
	}
	if revoked.Valid {
		return fmt.Errorf("outbox: %s has been unpaired", deviceID)
	}
	return nil
}

// Run hashes queued files until ctx is cancelled.
//
// It sweeps once at startup as well as on every Add: a receiver that was
// stopped mid-hash comes back to rows still marked pending, and nothing else
// would ever wake them.
func (q *Queue) Run(ctx context.Context) {
	for {
		if _, err := q.HashPending(ctx); err != nil && ctx.Err() == nil {
			q.log.Warn("could not prepare queued files", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
	}
}

// HashPending hashes every item waiting for a digest and reports how many it
// finished. Exported because it is the whole of the worker's behaviour, and a
// test that has to race a goroutine to observe it is a test that will flake.
func (q *Queue) HashPending(ctx context.Context) (int, error) {
	done := 0
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}

		item, err := q.nextPending(ctx)
		if errors.Is(err, ErrNotFound) {
			return done, nil
		}
		if err != nil {
			return done, err
		}

		if err := q.hash(ctx, item); err != nil {
			return done, err
		}
		done++
	}
}

func (q *Queue) nextPending(ctx context.Context) (Item, error) {
	return q.scanOne(ctx, `SELECT `+columns+` FROM outbox
		WHERE state = 'pending' ORDER BY queued_at, id LIMIT 1`)
}

// hash reads the file once and records the digest, the size, and the mtime it
// saw. Failure is a state, not an error: a file that has been moved since it
// was queued must show up in the window saying so, and must not stop the rest
// of the queue.
func (q *Queue) hash(ctx context.Context, item Item) error {
	digest, info, err := hashFile(ctx, item.SourcePath)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return q.fail(ctx, item.ID, err.Error())
	}

	_, err = q.db.SQL().ExecContext(ctx, `
		UPDATE outbox
		SET sha256 = ?, size = ?, source_mtime = ?, state = 'ready', error = ''
		WHERE id = ? AND state = 'pending'`,
		digest, info.Size(), format(info.ModTime().UTC()), item.ID)
	if err != nil {
		return fmt.Errorf("outbox: record digest: %w", err)
	}
	return nil
}

// hashFile computes the digest the phone will verify against.
//
// SHA-256 rather than the BLAKE3 used everywhere else in this codebase. That
// digest is the receiver's own bookkeeping and never leaves it; this one is
// recomputed on an iPhone, where CryptoKit's SHA-256 runs on the CPU's crypto
// instructions and a second hash implementation shipped in Swift would be a
// correctness risk in exchange for nothing. See docs/DECISIONS.md.
func hashFile(ctx context.Context, path string) (string, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("%s is a folder", path)
	}

	h := sha256.New()
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, err
		}
	}

	// Re-stat after reading. A file that grew while it was being hashed would
	// otherwise be recorded with the size it started at and a digest covering
	// something else.
	after, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return "", nil, errors.New("the file changed while it was being read")
	}

	return hex.EncodeToString(h.Sum(nil)), after, nil
}

func (q *Queue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("outbox: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func format(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return format(*t)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func trimmed(s string) string { return strings.TrimSpace(s) }
