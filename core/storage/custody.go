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

package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Proving custody, which is the only thing that may authorise a phone to
// delete its copy of a photograph.
//
// Everywhere else in this product a failure costs bandwidth: a dedup probe
// that answers wrongly re-sends a file, a conversion that fails leaves the
// original. Here a wrong answer destroys the last copy of something nobody
// can take again. So this file assumes nothing and re-reads the bytes.
//
// In particular it does not trust:
//
//   - the ledger's own hash. It records what arrived, which is a statement
//     about the past. A disk that lost the file, a user who moved it, and a
//     space-saving conversion that replaced it all leave that column intact.
//   - the upload response. It was correct when it was sent, and the phone may
//     be acting on it a week later.
//   - the client's path. It is looked up as a key scoped to the authenticated
//     device, and the row's own path is what gets opened -- never the string
//     that came off the network joined onto the destination directory.

// Reasons custody was refused. They exist to be shown to a person: somebody
// who turned this on and then saw nothing deleted is entitled to know which
// of these happened.
const (
	// CustodyOK is the reason on a confirmed file: there isn't one.
	CustodyOK = ""

	// CustodyUnknown means no file with that path belongs to this device.
	// Also the answer for another device's file -- the difference between
	// "not yours" and "does not exist" is itself information (PROTOCOL §7).
	CustodyUnknown = "unknown"

	// CustodyOriginalRemoved means a space-saving conversion deleted the
	// bytes that arrived. The ledger's hash still describes them and is still
	// truthful; this receiver simply cannot produce them any more, so it is
	// in no position to authorise deleting anybody else's copy.
	CustodyOriginalRemoved = "original_removed"

	// CustodyMissing means the ledger has the file and the disk does not.
	CustodyMissing = "missing"

	// CustodySizeMismatch means the file on disk is not the length the ledger
	// recorded, or not the length the client is asking about.
	CustodySizeMismatch = "size_mismatch"

	// CustodyContentMismatch means the bytes on disk are the right length and
	// the wrong content. Rare and serious: silent corruption, or a file
	// replaced in place.
	CustodyContentMismatch = "content_mismatch"

	// CustodyUnreadable means the file could not be read at all.
	CustodyUnreadable = "unreadable"

	// CustodyBadRequest means the item did not carry a usable digest.
	CustodyBadRequest = "bad_request"
)

// CustodyRequest asks whether this receiver can still produce one file.
type CustodyRequest struct {
	// ID is the client's own key for the item. Opaque here, echoed back so a
	// batched answer can be matched up.
	ID string

	// Path is the destination-relative path this receiver reported when the
	// file was stored. It is a lookup key, not a path to open.
	Path string

	// Size the client believes the file to be.
	Size int64

	// SHA256 is the hex digest the client computed over its own copy.
	//
	// SHA-256 and not the BLAKE3 of the upload path, for the same reason the
	// outbox uses it: this is a digest a phone has to compute, and iOS does
	// SHA-256 on the CPU's crypto instructions through CryptoKit. BLAKE3
	// remains the authority wherever the receiver is the one hashing.
	SHA256 string
}

// CustodyResult is the answer for one request.
type CustodyResult struct {
	ID        string
	Confirmed bool

	// Reason is empty when confirmed, and one of the Custody* constants
	// otherwise.
	Reason string
}

// maxCustodyRead bounds one confirmation. Nothing in this product produces a
// file anywhere near it; it is here so that a corrupted ledger row pointing at
// a device node cannot hang a request forever.
const maxCustodyRead = 1 << 40 // 1 TiB

// Confirm answers whether this receiver still holds the exact bytes the client
// describes, for a file belonging to that client.
//
// It returns a result for every request. An error is returned only when the
// ledger itself could not be consulted: a file that is missing, truncated, or
// wrong is a refusal, not a failure, because the client must be able to tell
// those apart from "ask again later" and must never treat one as the other.
func (s *Store) Confirm(ctx context.Context, deviceID string, reqs []CustodyRequest) ([]CustodyResult, error) {
	if deviceID == "" {
		return nil, errors.New("confirm: device id is required")
	}

	out := make([]CustodyResult, 0, len(reqs))
	for _, req := range reqs {
		res, err := s.confirmOne(ctx, deviceID, req)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

func (s *Store) confirmOne(ctx context.Context, deviceID string, req CustodyRequest) (CustodyResult, error) {
	refuse := func(reason string) (CustodyResult, error) {
		return CustodyResult{ID: req.ID, Reason: reason}, nil
	}

	if !validSHA256(req.SHA256) || req.Path == "" || req.Size < 0 {
		return refuse(CustodyBadRequest)
	}

	var (
		fileID    int64
		storedRel string
		size      int64
		removedAt sql.NullString
	)
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT id, stored_path, size, original_removed_at
		   FROM files
		  WHERE device_id = ? AND stored_path = ?`,
		deviceID, req.Path,
	).Scan(&fileID, &storedRel, &size, &removedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return refuse(CustodyUnknown)
	case err != nil:
		return CustodyResult{}, fmt.Errorf("confirm custody: %w", err)
	}

	// Checked before the disk is touched. A space-saving conversion removed
	// these bytes deliberately, and whatever is at that path now -- an H.264
	// re-encode under the same name, nothing at all -- is not what the phone
	// is holding (docs/DECISIONS.md).
	if removedAt.Valid && removedAt.String != "" {
		return refuse(CustodyOriginalRemoved)
	}

	if size != req.Size {
		return refuse(CustodySizeMismatch)
	}

	abs, ok := s.resolve(storedRel)
	if !ok {
		// The ledger's own path escapes the destination directory. Nothing in
		// this codebase writes such a row, which is exactly why it is worth
		// refusing rather than opening.
		return refuse(CustodyUnknown)
	}

	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return refuse(CustodyMissing)
	case err != nil:
		return refuse(CustodyUnreadable)
	case !info.Mode().IsRegular():
		return refuse(CustodyMissing)
	case info.Size() != size:
		// Truncated, appended to, or replaced. Caught here so that a file
		// which is obviously wrong is not read in full to say so.
		return refuse(CustodySizeMismatch)
	}

	digest, err := sha256OfFile(ctx, abs)
	if err != nil {
		if ctx.Err() != nil {
			return CustodyResult{}, ctx.Err()
		}
		return refuse(CustodyUnreadable)
	}
	if !strings.EqualFold(digest, req.SHA256) {
		return refuse(CustodyContentMismatch)
	}

	if err := s.markConfirmed(ctx, fileID); err != nil {
		return CustodyResult{}, err
	}
	return CustodyResult{ID: req.ID, Confirmed: true, Reason: CustodyOK}, nil
}

// resolve turns a destination-relative ledger path into an absolute one,
// reporting whether it stays inside the destination directory.
func (s *Store) resolve(rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) || filepath.IsAbs(filepath.FromSlash(rel)) {
		return "", false
	}
	abs := filepath.Join(s.root, filepath.FromSlash(rel))
	if abs != s.root && !strings.HasPrefix(abs, s.root+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

// markConfirmed records that the proof was given. It is deliberately not a
// cache: the next request re-reads the file regardless.
func (s *Store) markConfirmed(ctx context.Context, fileID int64) error {
	_, err := s.db.SQL().ExecContext(ctx,
		`UPDATE files SET custody_confirmed_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), fileID)
	if err != nil {
		return fmt.Errorf("record custody confirmation: %w", err)
	}
	return nil
}

func sha256OfFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	// A confirmation of a large library is a lot of reading, and a user who
	// cancels expects it to stop rather than to finish quietly.
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: io.LimitReader(f, maxCustodyRead)}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ctxReader makes a plain file read cancellable.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
