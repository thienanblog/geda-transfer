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

package receiver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	tus "github.com/tus/tusd/v2/pkg/handler"

	"github.com/geda/geda-transfer/core/hash"
	"github.com/geda/geda-transfer/core/storage"
)

// uploadStore is the tus DataStore backing /v1/files.
//
// tusd owns the protocol -- offsets, resumption, and status codes -- while
// this type owns the bytes. It hashes while writing rather than reading the
// finished file back, which matters on a NAS where a second pass over every
// received video is real time on real disks.
type uploadStore struct {
	files *storage.Store
	dir   string

	// running holds in-progress hashers keyed by upload id. A hasher only
	// helps while an upload proceeds in order within one process; anything
	// else falls back to hashing the finished file.
	mu      sync.Mutex
	running map[string]*runningHash
}

// runningHash tracks how much of an upload has been folded into a hasher.
type runningHash struct {
	h      *hash.Hasher
	offset int64
}

func newUploadStore(files *storage.Store) *uploadStore {
	return &uploadStore{
		files:   files,
		dir:     files.IncomingDir(),
		running: make(map[string]*runningHash),
	}
}

func (s *uploadStore) binPath(id string) string  { return filepath.Join(s.dir, id+".bin") }
func (s *uploadStore) infoPath(id string) string { return filepath.Join(s.dir, id+".info") }

func (s *uploadStore) NewUpload(ctx context.Context, info tus.FileInfo) (tus.Upload, error) {
	id, err := newUploadID()
	if err != nil {
		return nil, err
	}
	info.ID = id

	f, err := os.OpenFile(s.binPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create upload: %w", err)
	}
	f.Close()

	u := &upload{store: s, info: info}
	if err := u.writeInfo(); err != nil {
		os.Remove(s.binPath(id))
		return nil, err
	}
	return u, nil
}

func (s *uploadStore) GetUpload(ctx context.Context, id string) (tus.Upload, error) {
	// The id comes from a URL. Anything that is not a plain hex id could
	// otherwise reach the filesystem as a path.
	if !validUploadID(id) {
		return nil, tus.ErrNotFound
	}

	raw, err := os.ReadFile(s.infoPath(id))
	if os.IsNotExist(err) {
		return nil, tus.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read upload info: %w", err)
	}

	var info tus.FileInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("parse upload info: %w", err)
	}

	stat, err := os.Stat(s.binPath(id))
	if err != nil {
		return nil, tus.ErrNotFound
	}
	info.Offset = stat.Size()

	return &upload{store: s, info: info}, nil
}

// AsTerminatableUpload lets a client abandon an upload with DELETE.
func (s *uploadStore) AsTerminatableUpload(u tus.Upload) tus.TerminatableUpload {
	return u.(*upload)
}

func (s *uploadStore) hasherFor(id string, offset int64) *hash.Hasher {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.running[id]; ok && r.offset == offset {
		return r.h
	}
	if offset != 0 {
		// Resuming somewhere this process has no state for. Give up on
		// incremental hashing; the finished file will be read instead.
		delete(s.running, id)
		return nil
	}

	h := hash.New()
	s.running[id] = &runningHash{h: h}
	return h
}

func (s *uploadStore) advanceHasher(id string, offset int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.running[id]; ok {
		r.offset = offset
	}
}

func (s *uploadStore) takeHasher(id string, size int64) *hash.Hasher {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.running[id]
	delete(s.running, id)
	if !ok || r.offset != size {
		return nil
	}
	return r.h
}

func (s *uploadStore) forgetHasher(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}

// upload is one in-flight file.
type upload struct {
	store *uploadStore
	info  tus.FileInfo
}

func (u *upload) GetInfo(ctx context.Context) (tus.FileInfo, error) { return u.info, nil }

func (u *upload) writeInfo() error {
	raw, err := json.Marshal(u.info)
	if err != nil {
		return fmt.Errorf("encode upload info: %w", err)
	}
	if err := os.WriteFile(u.store.infoPath(u.info.ID), raw, 0o600); err != nil {
		return fmt.Errorf("write upload info: %w", err)
	}
	return nil
}

func (u *upload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	f, err := os.OpenFile(u.store.binPath(u.info.ID), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open upload: %w", err)
	}
	defer f.Close()

	// tusd validates the offset before calling, but this file is opened for
	// append: if the two ever disagreed the bytes would be written at the
	// wrong place and the corruption would only surface as a hash mismatch at
	// the very end, after the whole file had been transferred.
	stat, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat upload: %w", err)
	}
	if stat.Size() != offset {
		return 0, fmt.Errorf("upload %s is at offset %d, chunk claims %d",
			u.info.ID, stat.Size(), offset)
	}

	var dst io.Writer = f
	if h := u.store.hasherFor(u.info.ID, offset); h != nil {
		// Hash as the bytes land, so the common case of a single streamed
		// PATCH never needs a second pass over the file.
		dst = io.MultiWriter(f, h)
	}

	n, err := io.Copy(dst, src)
	if n > 0 {
		u.info.Offset = offset + n
		u.store.advanceHasher(u.info.ID, u.info.Offset)
	}
	if err != nil {
		return n, err
	}
	return n, nil
}

func (u *upload) GetReader(ctx context.Context) (io.ReadCloser, error) {
	return os.Open(u.store.binPath(u.info.ID))
}

func (u *upload) Terminate(ctx context.Context) error {
	u.store.forgetHasher(u.info.ID)
	os.Remove(u.store.binPath(u.info.ID))
	os.Remove(u.store.infoPath(u.info.ID))
	return nil
}

// FinishUpload verifies the content and hands it to storage.
//
// The hash is the authority: a mismatch means the file is discarded, never
// stored. Only a verified file may later authorise deleting the original from
// the phone.
func (u *upload) FinishUpload(ctx context.Context) error {
	id := u.info.ID
	bin := u.store.binPath(id)

	digest, err := u.digest(ctx, id, bin)
	if err != nil {
		return err
	}

	if declared := u.info.MetaData["hash"]; declared != "" && declared != digest.Full {
		u.store.forgetHasher(id)
		os.Remove(bin)
		os.Remove(u.store.infoPath(id))
		return errChecksumMismatch
	}

	in, err := incomingFrom(u.info, digest)
	if err != nil {
		return err
	}

	committed, err := u.store.files.Commit(ctx, in, bin)
	if err != nil {
		return fmt.Errorf("commit upload: %w", err)
	}

	if u.info.MetaData == nil {
		u.info.MetaData = make(tus.MetaData, 2)
	}
	u.info.MetaData["stored_path"] = committed.Path
	if committed.Deduplicated {
		u.info.MetaData["deduplicated"] = "1"
	}
	_ = u.writeInfo()

	os.Remove(u.store.infoPath(id))
	return nil
}

// digest returns the upload's digest, reusing the running hasher when it
// covers the whole file and re-reading otherwise.
func (u *upload) digest(ctx context.Context, id, bin string) (hash.Digest, error) {
	if h := u.store.takeHasher(id, u.info.Size); h != nil {
		return h.Digest(), nil
	}

	d, err := hash.File(ctx, bin)
	if err != nil {
		return hash.Digest{}, fmt.Errorf("verify upload: %w", err)
	}
	return d, nil
}

// errChecksumMismatch maps to the tus checksum failure status.
var errChecksumMismatch = tus.NewError("ERR_CHECKSUM_MISMATCH",
	"content does not match the declared hash", 460)

// incomingFrom translates tus metadata into a storage request.
func incomingFrom(info tus.FileInfo, digest hash.Digest) (storage.Incoming, error) {
	md := info.MetaData

	in := storage.Incoming{
		DeviceID:     md["device_id"],
		DeviceName:   md["device_name"],
		OriginalName: md["filename"],
		Album:        md["album"],
		PairID:       md["pair_id"],
		PairRole:     md["pair_role"],
		Kind:         md["kind"],
		Digest:       digest,
	}

	if in.OriginalName == "" {
		return storage.Incoming{}, tus.NewError("ERR_MISSING_FILENAME",
			"Upload-Metadata must include filename", 400)
	}
	if in.Kind == "" {
		in.Kind = storage.KindFile
	}

	if raw := md["captured_at"]; raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return storage.Incoming{}, tus.NewError("ERR_BAD_CAPTURED_AT",
				"captured_at must be RFC3339", 400)
		}
		in.CapturedAt = t
	}

	switch in.PairRole {
	case "", storage.RolePrimary, storage.RoleSecondary:
	default:
		return storage.Incoming{}, tus.NewError("ERR_BAD_PAIR_ROLE",
			"pair_role must be primary or secondary", 400)
	}

	return in, nil
}

func newUploadID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate upload id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func validUploadID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// sweepIncoming removes partial uploads older than maxAge. Without it an
// abandoned upload occupies disk forever, which on a NAS is the difference
// between a working backup and a full volume.
func (s *uploadStore) sweepIncoming(maxAge time.Duration) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("sweep incoming: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	var errs []error
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
