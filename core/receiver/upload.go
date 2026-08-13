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

	"github.com/geda/geda-transfer/core/events"
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

	// bus carries the lifecycle of each upload to whoever is watching, which
	// on the desktop is a live transfer view. Nil when nobody is.
	bus *events.Bus

	// running holds in-progress hashers keyed by upload id. A hasher only
	// helps while an upload proceeds in order within one process; anything
	// else falls back to hashing the finished file.
	mu      sync.Mutex
	running map[string]*runningHash

	// announced remembers which uploads this process has already reported as
	// started, so a client sending one file in several PATCHes produces one
	// start and not one per chunk. The value is when it was recorded, so that
	// the sweep can drop entries for uploads nobody ever came back to finish.
	announced map[string]time.Time
}

// runningHash tracks how much of an upload has been folded into a hasher.
type runningHash struct {
	h      *hash.Hasher
	offset int64
}

func newUploadStore(files *storage.Store, bus *events.Bus) *uploadStore {
	return &uploadStore{
		files:     files,
		dir:       files.IncomingDir(),
		bus:       bus,
		running:   make(map[string]*runningHash),
		announced: make(map[string]time.Time),
	}
}

// eventFor builds the fields every event about one upload shares.
//
// device_id and device_name are the authenticated ones: PreUploadCreateCallback
// overwrites whatever the client claimed before the upload is created, so a
// watcher cannot be shown a peer's name (docs/DECISIONS.md).
func eventFor(info tus.FileInfo) events.Event {
	return events.Event{
		Direction:  events.DirectionInbound,
		UploadID:   info.ID,
		DeviceID:   info.MetaData["device_id"],
		DeviceName: info.MetaData["device_name"],
		Name:       info.MetaData["filename"],
		AssetKind:  info.MetaData["kind"],
		Size:       info.Size,
	}
}

// announce publishes a start the first time this process sees an upload move.
//
// It is keyed per process rather than per upload because a resume arrives at a
// receiver that may have been restarted since the upload was created, and a
// live view that never learns about the file would show the transfer as idle
// while it is in fact running.
func (s *uploadStore) announce(info tus.FileInfo, offset int64) {
	if s.bus == nil {
		return
	}

	s.mu.Lock()
	_, seen := s.announced[info.ID]
	if !seen {
		s.announced[info.ID] = time.Now()
	}
	s.mu.Unlock()

	if seen {
		return
	}

	e := eventFor(info)
	e.Kind = events.KindStarted
	e.Offset = offset
	s.bus.Publish(e)
}

// finish publishes the end of an upload and forgets the per-process state.
func (s *uploadStore) finish(e events.Event) {
	s.mu.Lock()
	delete(s.announced, e.UploadID)
	delete(s.running, e.UploadID)
	s.mu.Unlock()

	s.bus.Publish(e)
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

	// Announced at creation rather than at the first byte so that a watcher
	// sees the file appear while the phone is still opening the connection,
	// which is what makes a queue of small files look like it is moving.
	s.announce(info, 0)
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

	// A resume reaches a process that may never have seen this upload -- the
	// receiver can have been restarted since it was created.
	u.store.announce(u.info, offset)

	writers := make([]io.Writer, 1, 3)
	writers[0] = f
	if h := u.store.hasherFor(u.info.ID, offset); h != nil {
		// Hash as the bytes land, so the common case of a single streamed
		// PATCH never needs a second pass over the file.
		writers = append(writers, h)
	}

	// One PATCH carries a whole file, so progress has to be reported from
	// inside the copy. Without this a 4K video is one event at the end.
	var progress *events.Progress
	if u.store.bus != nil {
		progress = events.NewProgress(u.store.bus, eventFor(u.info), offset)
		writers = append(writers, progress)
	}

	var dst io.Writer = f
	if len(writers) > 1 {
		dst = io.MultiWriter(writers...)
	}

	n, err := io.Copy(dst, src)
	if n > 0 {
		u.info.Offset = offset + n
		u.store.advanceHasher(u.info.ID, u.info.Offset)
	}
	if progress != nil && n > 0 {
		// The interval will usually have swallowed the last few megabytes.
		progress.Flush()
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
	os.Remove(u.store.binPath(u.info.ID))
	os.Remove(u.store.infoPath(u.info.ID))

	// A DELETE is the sending device saying it has given up, which is not the
	// same as an interruption: an interrupted upload keeps its bytes and can
	// be resumed, so it is deliberately not reported as a failure.
	e := eventFor(u.info)
	e.Kind = events.KindFailed
	e.Offset = u.info.Offset
	e.Error = "the sending device cancelled the upload"
	u.store.finish(e)
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
		return u.failed(err)
	}

	if declared := u.info.MetaData["hash"]; declared != "" && declared != digest.Full {
		os.Remove(bin)
		os.Remove(u.store.infoPath(id))
		return u.failed(errChecksumMismatch)
	}

	in, err := incomingFrom(u.info, digest)
	if err != nil {
		return u.failed(err)
	}

	committed, err := u.store.files.Commit(ctx, in, bin)
	if err != nil {
		return u.failed(fmt.Errorf("commit upload: %w", err))
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

	e := eventFor(u.info)
	e.Kind = events.KindFinished
	e.Offset = u.info.Size
	e.StoredPath = committed.Path
	e.Deduplicated = committed.Deduplicated
	u.store.finish(e)
	return nil
}

// failed reports an upload that will not produce a file, and returns the error
// unchanged so callers can stay a one-liner.
//
// Every exit from FinishUpload other than success goes through here: a silent
// failure leaves a row in the live view that never resolves, which reads to
// the user as a transfer that is still running.
func (u *upload) failed(err error) error {
	e := eventFor(u.info)
	e.Kind = events.KindFailed
	e.Offset = u.info.Offset
	e.Error = err.Error()
	u.store.finish(e)
	return err
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

	// An upload that is created and then abandoned leaves an entry behind in
	// both in-memory maps. On a NAS running for months that is a slow leak,
	// so it is cleared on the same schedule as the bytes it describes.
	s.mu.Lock()
	for id, at := range s.announced {
		if at.Before(cutoff) {
			delete(s.announced, id)
			delete(s.running, id)
		}
	}
	s.mu.Unlock()

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
