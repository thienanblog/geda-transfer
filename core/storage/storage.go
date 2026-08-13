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

// Package storage puts received files where the user asked for them.
//
// It owns the three rules that decide a file's final name (AGENTS.md §3.6):
//
//   - Identical content is skipped rather than stored twice.
//   - A name collision between different content appends _1, _2, and so on.
//   - Members of a Live Photo or RAW+JPEG pair share one basename, allocated
//     once for the pair.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/geda/geda-transfer/core/formats"
	"github.com/geda/geda-transfer/core/hash"
	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/store"
)

// SettingTemplate is the settings key holding the user's filename template.
const SettingTemplate = "naming_template"

// incomingDir holds partial uploads. It sits inside the destination root so
// that finishing an upload is a rename within one filesystem, which is atomic,
// rather than a copy across devices.
const incomingDir = ".incoming"

// maxCollisionAttempts bounds the _1, _2, ... search. Reaching it means either
// a template with no distinguishing variables or something pathological, and
// failing loudly beats spinning.
const maxCollisionAttempts = 10_000

// Kinds a file may be recorded as.
const (
	KindPhoto = "photo"
	KindVideo = "video"
	KindFile  = "file"
)

// Pair roles.
const (
	RolePrimary   = "primary"
	RoleSecondary = "secondary"
)

// ErrNoSpace reports that the collision search gave up.
var ErrNoSpace = errors.New("no free filename found")

// Store writes received files into a destination directory and records them
// in the ledger.
type Store struct {
	db   *store.DB
	root string

	// wake is called after a conversion is queued, so the worker starts on it
	// rather than waiting for its next sweep. Nil when nothing is converting.
	wake func()
}

// New prepares root as a destination directory.
func New(db *store.DB, root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve destination %s: %w", root, err)
	}
	if err := os.MkdirAll(filepath.Join(abs, incomingDir), 0o700); err != nil {
		return nil, fmt.Errorf("prepare destination %s: %w", abs, err)
	}
	return &Store{db: db, root: abs}, nil
}

// Root is the absolute destination directory.
func (s *Store) Root() string { return s.root }

// IncomingDir is where partial uploads live.
func (s *Store) IncomingDir() string { return filepath.Join(s.root, incomingDir) }

// Template returns the user's filename template, or the default if unset.
func (s *Store) Template(ctx context.Context) (string, error) {
	tmpl, err := s.db.Setting(ctx, SettingTemplate)
	if errors.Is(err, store.ErrNotFound) {
		return naming.Default, nil
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(tmpl) == "" {
		return naming.Default, nil
	}
	return tmpl, nil
}

// Policy returns the user's output policy, or the default if unset.
//
// It is read per commit rather than cached, for the same reason the template
// is: a settings change has to reach the next file, not the next restart.
func (s *Store) Policy(ctx context.Context) formats.Policy {
	preset, err := s.db.Setting(ctx, formats.SettingPreset)
	if err != nil {
		// Including ErrNotFound, which is the common case on a receiver
		// nobody has configured. A policy that cannot be read is the default
		// one, and the default keeps originals.
		return formats.Default()
	}
	matrix, err := s.db.Setting(ctx, formats.SettingMatrix)
	if err != nil {
		matrix = ""
	}
	return formats.Unmarshal(preset, matrix)
}

// NotifyConversions registers a callback run whenever a conversion is queued.
//
// Without it the converter would still do the work, on its next sweep. With
// it, a photo that lands while somebody is watching the window is converted
// straight away, which is the difference between a feature that looks broken
// and one that looks instant.
func (s *Store) NotifyConversions(f func()) { s.wake = f }

// Incoming describes a fully received file awaiting a home.
type Incoming struct {
	DeviceID   string
	DeviceName string

	// OriginalName is the filename on the sending device. Untrusted.
	OriginalName string

	// CapturedAt is the asset's capture date. It becomes the stored file's
	// mtime, so that a file browser sorts by when the photo was taken rather
	// than when it was copied.
	CapturedAt time.Time

	// Album is the source album, if any. Untrusted.
	Album string

	// PairID groups a Live Photo or RAW+JPEG pair. Empty for a lone file.
	PairID   string
	PairRole string

	Kind   string
	Digest hash.Digest
}

// Committed reports where a file ended up.
type Committed struct {
	// Path is relative to the destination root, using forward slashes.
	Path string

	// Deduplicated is true when identical content was already present, in
	// which case Path points at the existing copy and nothing was written.
	Deduplicated bool

	// Conversion is what the output policy decided about this file. Its
	// action is formats.ActionKeep when nothing was queued, which is the
	// default preset's answer for everything.
	Conversion formats.Decision
}

// AbsPath returns the committed file's absolute location.
func (s *Store) AbsPath(c Committed) string {
	return filepath.Join(s.root, filepath.FromSlash(c.Path))
}

// Commit moves a fully received file at tempPath into its final home and
// records it in the ledger.
//
// The caller must have verified that the content hashes to in.Digest. On
// success tempPath no longer exists; on failure it is left alone so the caller
// can retry or clean up.
func (s *Store) Commit(ctx context.Context, in Incoming, tempPath string) (Committed, error) {
	if in.DeviceID == "" {
		return Committed{}, errors.New("commit: device id is required")
	}
	if in.Digest.Full == "" {
		return Committed{}, errors.New("commit: digest is required")
	}
	switch in.Kind {
	case KindPhoto, KindVideo, KindFile:
	default:
		return Committed{}, fmt.Errorf("commit: unknown kind %q", in.Kind)
	}

	// Identical content already on disk is the common case on a re-run, so it
	// is checked before any name is allocated.
	if existing, err := s.existingPath(ctx, in.DeviceID, in.Digest.Full); err != nil {
		return Committed{}, err
	} else if existing != "" {
		if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
			return Committed{}, fmt.Errorf("discard duplicate upload: %w", err)
		}
		// Nothing was written, so there is nothing new to convert. The copy
		// already on disk was converted when it arrived; queueing it again
		// would redo that work on every re-run of the same library.
		return Committed{
			Path:         existing,
			Deduplicated: true,
			Conversion: formats.Decision{
				Class:  formats.Classify(in.Kind, existing),
				Action: formats.ActionKeep,
			},
		}, nil
	}

	tmpl, err := s.Template(ctx)
	if err != nil {
		return Committed{}, err
	}

	placed, fileID, err := s.place(ctx, in, tmpl)
	if err != nil {
		return Committed{}, err
	}

	// The destination was claimed with O_EXCL, so this rename replaces a
	// placeholder this process created rather than clobbering a file that was
	// already there.
	abs := filepath.Join(s.root, filepath.FromSlash(placed))
	if err := os.Rename(tempPath, abs); err != nil {
		s.unplace(ctx, in.DeviceID, placed, abs)
		return Committed{}, fmt.Errorf("move into place: %w", err)
	}

	if !in.CapturedAt.IsZero() {
		// A failure here is cosmetic: the bytes are safe and the ledger is
		// correct, so it must not fail the transfer.
		_ = os.Chtimes(abs, in.CapturedAt, in.CapturedAt)
	}

	// Last, and deliberately after the file is in its final place: a worker
	// that found a row pointing at a name still being renamed into would
	// convert a file that is not there yet.
	decision := s.queueConversion(ctx, in, placed, fileID)

	return Committed{Path: placed, Conversion: decision}, nil
}

// queueConversion asks the output policy what happens to this file and, if
// the answer is work, records it.
//
// Every failure here is swallowed after logging into the returned decision:
// the transfer succeeded, the bytes are stored, and refusing to acknowledge a
// file the receiver is holding because a queue insert failed would make the
// phone send it all over again.
func (s *Store) queueConversion(ctx context.Context, in Incoming, placed string, fileID int64) formats.Decision {
	decision := s.Policy(ctx).Decide(formats.File{
		Name:   placed,
		Kind:   in.Kind,
		Paired: in.PairID != "",
	})
	if decision.Action == formats.ActionKeep {
		return decision
	}

	err := formats.Enqueue(ctx, s.db, formats.Request{
		FileID:     fileID,
		DeviceID:   in.DeviceID,
		SourcePath: placed,
		Class:      decision.Class,
		Action:     decision.Action,
		Note:       decision.Downgraded,
		CapturedAt: in.CapturedAt,
	})
	if err != nil {
		decision.Action = formats.ActionKeep
		decision.Downgraded = err.Error()
		return decision
	}

	if s.wake != nil {
		s.wake()
	}
	return decision
}

// existingPath reports where identical content from this device already lives,
// or "" if it does not.
func (s *Store) existingPath(ctx context.Context, deviceID, fullHash string) (string, error) {
	var path string
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT stored_path FROM files WHERE device_id = ? AND hash = ? LIMIT 1`,
		deviceID, fullHash).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("dedup lookup: %w", err)
	}
	return path, nil
}

// place picks a free name, claims it on disk and in the ledger, and returns
// the destination-relative path.
func (s *Store) place(ctx context.Context, in Incoming, tmpl string) (string, int64, error) {
	vars := naming.Vars{
		CapturedAt:   in.CapturedAt,
		OriginalName: in.OriginalName,
		Device:       in.DeviceName,
		Album:        in.Album,
		Hash:         in.Digest.Full,
	}

	// A pair member that is not the first to arrive must reuse the basename
	// the pair already reserved, whichever member reserved it.
	if in.PairID != "" {
		reserved, ok, err := s.reservedPairName(ctx, in.DeviceID, in.PairID)
		if err != nil {
			return "", 0, err
		}
		if ok {
			res, err := naming.Render(tmpl, vars, 0)
			if err != nil {
				return "", 0, fmt.Errorf("render template: %w", err)
			}

			path, fileID, err := s.claim(ctx, in, naming.Result{
				Dir:  reserved.dir,
				Base: reserved.basename,
				Ext:  res.Ext,
			})
			if err == nil {
				return path, fileID, nil
			}
			if !errors.Is(err, errNameTaken) {
				return "", 0, err
			}
			// Two members of one pair claim the same extension, so they cannot
			// both have the pair's name. That should not happen -- a Live
			// Photo is one HEIC and one MOV -- but keeping the bytes matters
			// more than keeping the grouping, so fall through and give this
			// one a counter rather than failing the transfer.
		}
	}

	for counter := range maxCollisionAttempts {
		res, err := naming.Render(tmpl, vars, counter)
		if err != nil {
			return "", 0, fmt.Errorf("render template: %w", err)
		}

		taken, err := s.basenameTaken(ctx, res.Dir, res.Base)
		if err != nil {
			return "", 0, err
		}
		if taken {
			continue
		}

		path, fileID, err := s.claim(ctx, in, res)
		if errors.Is(err, errNameTaken) {
			continue
		}
		if err != nil {
			return "", 0, err
		}
		return path, fileID, nil
	}

	return "", 0, fmt.Errorf("%w after %d attempts", ErrNoSpace, maxCollisionAttempts)
}

// errNameTaken signals that a candidate name was claimed by someone else
// between the check and the claim.
var errNameTaken = errors.New("name taken")

type pairName struct {
	dir      string
	basename string
}

func (s *Store) reservedPairName(ctx context.Context, deviceID, pairID string) (pairName, bool, error) {
	var p pairName
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT dir, basename FROM pair_basenames WHERE device_id = ? AND pair_id = ?`,
		deviceID, pairID).Scan(&p.dir, &p.basename)
	if errors.Is(err, sql.ErrNoRows) {
		return pairName{}, false, nil
	}
	if err != nil {
		return pairName{}, false, fmt.Errorf("pair lookup: %w", err)
	}
	return p, true, nil
}

// basenameTaken reports whether any file already uses this basename in this
// directory, regardless of extension.
//
// The extension is deliberately ignored. A Live Photo occupies both IMG_1.HEIC
// and IMG_1.MOV, so checking the full filename would let an unrelated pair
// take IMG_1 and then collide on one of its two members.
func (s *Store) basenameTaken(ctx context.Context, dir, base string) (bool, error) {
	var n int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM files WHERE dir = ? AND basename = ?`, dir, base).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("collision probe: %w", err)
	}
	return n > 0, nil
}

// claim reserves a name on disk and in the ledger, returning errNameTaken if
// either was won by someone else.
func (s *Store) claim(ctx context.Context, in Incoming, res naming.Result) (string, int64, error) {
	rel := res.Path()
	abs := filepath.Join(s.root, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", 0, fmt.Errorf("create destination directory: %w", err)
	}

	// O_EXCL is what stops a rename from silently destroying a file the user
	// put here themselves, which the ledger would know nothing about.
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", 0, errNameTaken
		}
		return "", 0, fmt.Errorf("claim %s: %w", rel, err)
	}
	f.Close()

	fileID, err := s.record(ctx, in, res, rel)
	if err != nil {
		os.Remove(abs)
		return "", 0, err
	}
	return rel, fileID, nil
}

// record inserts the ledger row and, for a pair member arriving first, the
// pair's basename reservation. Both happen in one transaction so a crash
// cannot leave a reservation with no file behind it.
func (s *Store) record(ctx context.Context, in Incoming, res naming.Result, rel string) (int64, error) {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	var capturedAt any
	if !in.CapturedAt.IsZero() {
		capturedAt = in.CapturedAt.UTC().Format(time.RFC3339Nano)
	}
	var pairID, pairRole any
	if in.PairID != "" {
		pairID = in.PairID
		if in.PairRole != "" {
			pairRole = in.PairRole
		}
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO files (device_id, hash, head_hash, size, captured_at, original_name,
		                   dir, basename, ext, stored_path, pair_id, pair_role, kind, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.DeviceID, in.Digest.Full, in.Digest.Head, in.Digest.Size, capturedAt, in.OriginalName,
		res.Dir, res.Base, res.Ext, rel, pairID, pairRole, in.Kind, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, errNameTaken
		}
		return 0, fmt.Errorf("record file: %w", err)
	}

	fileID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("record file: %w", err)
	}

	if in.PairID != "" {
		// INSERT OR IGNORE, because the other member of the pair may have won
		// the race. Either way both members end up with the same basename.
		_, err = tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO pair_basenames (device_id, pair_id, dir, basename, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			in.DeviceID, in.PairID, res.Dir, res.Base, now)
		if err != nil {
			return 0, fmt.Errorf("reserve pair basename: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return fileID, nil
}

// unplace undoes a claim whose file could not be moved into place.
func (s *Store) unplace(ctx context.Context, deviceID, rel, abs string) {
	_, _ = s.db.SQL().ExecContext(context.WithoutCancel(ctx),
		`DELETE FROM files WHERE device_id = ? AND stored_path = ?`, deviceID, rel)
	_ = os.Remove(abs)
}

// SQLite extended result codes for the two constraint failures that mean
// "someone else already claimed this name".
const (
	sqliteConstraintUnique     = 2067
	sqliteConstraintPrimaryKey = 1555
)

func isUniqueViolation(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	switch serr.Code() {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return true
	default:
		return false
	}
}
