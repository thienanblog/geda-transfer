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

package storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/storage"
)

// The rule this file is about: a confirmation is a statement about the bytes
// on this disk right now, and every way of making that statement false has to
// produce a refusal rather than a confirmation.

func sha256Of(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// confirm asks about one stored file, with the digest and size of content.
func (f *fixture) confirm(rel, content string) storage.CustodyResult {
	f.t.Helper()
	return f.confirmAs("dev-1", storage.CustodyRequest{
		ID:     "item-1",
		Path:   rel,
		Size:   int64(len(content)),
		SHA256: sha256Of(content),
	})
}

func (f *fixture) confirmAs(deviceID string, req storage.CustodyRequest) storage.CustodyResult {
	f.t.Helper()
	results, err := f.Confirm(f.t.Context(), deviceID, []storage.CustodyRequest{req})
	if err != nil {
		f.t.Fatalf("Confirm: %v", err)
	}
	if len(results) != 1 {
		f.t.Fatalf("Confirm returned %d results, want 1", len(results))
	}
	return results[0]
}

func TestCustodyConfirmsAFileThatIsStillThere(t *testing.T) {
	f := newFixture(t)
	const content = "the photograph"

	got := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, content)

	res := f.confirm(got.Path, content)
	if !res.Confirmed {
		t.Fatalf("not confirmed: %s", res.Reason)
	}
	if res.ID != "item-1" {
		t.Fatalf("id %q, want the one that was asked about", res.ID)
	}
}

// Every one of these is a way the receiver could stop being able to produce
// the bytes it once received. None of them may confirm.
func TestCustodyIsRefusedWhenTheBytesAreNotThere(t *testing.T) {
	const content = "the photograph"

	cases := []struct {
		name string
		// breaks is applied after the file has been committed, and returns
		// the request to make. A zero request means "ask about it normally".
		breaks func(f *fixture, rel string) storage.CustodyRequest
		// device asking, when it is not the one that sent the file.
		device string
		reason string
	}{
		{
			name: "the file was deleted from the destination",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(rel))); err != nil {
					f.t.Fatal(err)
				}
				return storage.CustodyRequest{}
			},
			reason: storage.CustodyMissing,
		},
		{
			name: "the file was truncated",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				write(f, rel, content[:4])
				return storage.CustodyRequest{}
			},
			reason: storage.CustodySizeMismatch,
		},
		{
			name: "the file grew",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				write(f, rel, content+" and more")
				return storage.CustodyRequest{}
			},
			reason: storage.CustodySizeMismatch,
		},
		{
			// The one that a size check cannot catch and a ledger lookup
			// would sail straight past: same length, different bytes.
			name: "a byte changed and the length did not",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				corrupted := "the photogrXph"
				if len(corrupted) != len(content) {
					f.t.Fatal("this case is only meaningful at equal length")
				}
				write(f, rel, corrupted)
				return storage.CustodyRequest{}
			},
			reason: storage.CustodyContentMismatch,
		},
		{
			name: "a space-saving conversion removed the original",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				// The bytes are deliberately left in place: the point is that
				// the ledger's record of the removal is enough on its own.
				_, err := f.db.SQL().ExecContext(f.t.Context(),
					`UPDATE files SET original_removed_at = ? WHERE stored_path = ?`,
					time.Now().UTC().Format(time.RFC3339Nano), rel)
				if err != nil {
					f.t.Fatal(err)
				}
				return storage.CustodyRequest{}
			},
			reason: storage.CustodyOriginalRemoved,
		},
		{
			name: "another device asks about it",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				f.pair("dev-2", "Someone else's iPhone")
				return storage.CustodyRequest{
					ID: "item-1", Path: rel,
					Size: int64(len(content)), SHA256: sha256Of(content),
				}
			},
			device: "dev-2",
			reason: storage.CustodyUnknown,
		},
		{
			name: "the client's digest is for different content",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				return storage.CustodyRequest{
					ID: "item-1", Path: rel,
					Size: int64(len(content)), SHA256: sha256Of("a different photograph"),
				}
			},
			reason: storage.CustodyContentMismatch,
		},
		{
			name: "the client's size is not the stored size",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				return storage.CustodyRequest{
					ID: "item-1", Path: rel,
					Size: int64(len(content)) + 1, SHA256: sha256Of(content),
				}
			},
			reason: storage.CustodySizeMismatch,
		},
		{
			name: "no digest is offered at all",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				return storage.CustodyRequest{ID: "item-1", Path: rel, Size: int64(len(content))}
			},
			reason: storage.CustodyBadRequest,
		},
		{
			name: "the digest is not a digest",
			breaks: func(f *fixture, rel string) storage.CustodyRequest {
				return storage.CustodyRequest{
					ID: "item-1", Path: rel,
					Size: int64(len(content)), SHA256: strings.Repeat("z", 64),
				}
			},
			reason: storage.CustodyBadRequest,
		},
		{
			name: "a path this receiver never stored",
			breaks: func(f *fixture, _ string) storage.CustodyRequest {
				return storage.CustodyRequest{
					ID: "item-1", Path: "2026/07/never-existed.HEIC",
					Size: int64(len(content)), SHA256: sha256Of(content),
				}
			},
			reason: storage.CustodyUnknown,
		},
		{
			// The client's path is a lookup key and never a path to open, so
			// this fails as an unknown file rather than reading anything.
			name: "a path that climbs out of the destination",
			breaks: func(f *fixture, _ string) storage.CustodyRequest {
				return storage.CustodyRequest{
					ID: "item-1", Path: "../../etc/passwd",
					Size: int64(len(content)), SHA256: sha256Of(content),
				}
			},
			reason: storage.CustodyUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			got := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, content)

			req := tc.breaks(f, got.Path)
			if req.ID == "" {
				req = storage.CustodyRequest{
					ID: "item-1", Path: got.Path,
					Size: int64(len(content)), SHA256: sha256Of(content),
				}
			}
			device := tc.device
			if device == "" {
				device = "dev-1"
			}
			res := f.confirmAs(device, req)

			if res.Confirmed {
				t.Fatal("confirmed a file the receiver cannot produce")
			}
			if res.Reason != tc.reason {
				t.Fatalf("reason %q, want %q", res.Reason, tc.reason)
			}
		})
	}
}

// A ledger row whose path escapes the destination directory is not something
// this codebase writes, which is exactly why it is worth refusing to open.
func TestCustodyRefusesALedgerRowThatPointsOutsideTheDestination(t *testing.T) {
	f := newFixture(t)
	const content = "the photograph"

	got := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, content)

	outside := filepath.Join(t.TempDir(), "elsewhere.HEIC")
	if err := os.WriteFile(outside, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SQL().ExecContext(t.Context(),
		`UPDATE files SET stored_path = ? WHERE stored_path = ?`, "../elsewhere.HEIC", got.Path); err != nil {
		t.Fatal(err)
	}

	res := f.confirmAs("dev-1", storage.CustodyRequest{
		ID: "item-1", Path: "../elsewhere.HEIC",
		Size: int64(len(content)), SHA256: sha256Of(content),
	})
	if res.Confirmed {
		t.Fatal("confirmed a file outside the destination directory")
	}
}

// The confirmation is a fresh read every time, not a cached verdict. A file
// that was confirmed and then lost must stop being confirmed.
func TestCustodyIsNotCached(t *testing.T) {
	f := newFixture(t)
	const content = "the photograph"

	got := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, content)

	if res := f.confirm(got.Path, content); !res.Confirmed {
		t.Fatalf("first confirmation refused: %s", res.Reason)
	}

	write(f, got.Path, "something else entirely")

	if res := f.confirm(got.Path, content); res.Confirmed {
		t.Fatal("confirmed from a remembered verdict rather than the file")
	}
}

// A confirmation is a destructive action taken on this machine's word, so the
// machine records having given it.
func TestCustodyIsRecordedOnTheFile(t *testing.T) {
	f := newFixture(t)
	const content = "the photograph"

	got := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, content)

	var before *string
	if err := f.db.SQL().QueryRowContext(t.Context(),
		`SELECT custody_confirmed_at FROM files WHERE stored_path = ?`, got.Path).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != nil {
		t.Fatalf("a freshly received file is already marked confirmed: %q", *before)
	}

	if res := f.confirm(got.Path, content); !res.Confirmed {
		t.Fatalf("not confirmed: %s", res.Reason)
	}

	var after *string
	if err := f.db.SQL().QueryRowContext(t.Context(),
		`SELECT custody_confirmed_at FROM files WHERE stored_path = ?`, got.Path).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after == nil {
		t.Fatal("a confirmation left no trace on the file")
	}
}

// A refusal is an answer, not an error: the client has to be able to tell
// "this one is gone" from "ask again later", because it treats them
// differently and only one of them is permanent.
func TestCustodyAnswersEveryItemInABatch(t *testing.T) {
	f := newFixture(t)

	first := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, "one")
	second := f.commit(storage.Incoming{OriginalName: "IMG_2.HEIC", CapturedAt: captured}, "two")

	if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(second.Path))); err != nil {
		t.Fatal(err)
	}

	results, err := f.Confirm(t.Context(), "dev-1", []storage.CustodyRequest{
		{ID: "a", Path: first.Path, Size: 3, SHA256: sha256Of("one")},
		{ID: "b", Path: second.Path, Size: 3, SHA256: sha256Of("two")},
		{ID: "c", Path: "2026/07/nothing.HEIC", Size: 3, SHA256: sha256Of("three")},
	})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("%d results for 3 items", len(results))
	}

	want := []struct {
		id        string
		confirmed bool
	}{{"a", true}, {"b", false}, {"c", false}}
	for i, w := range want {
		if results[i].ID != w.id || results[i].Confirmed != w.confirmed {
			t.Fatalf("result %d: %+v, want id %s confirmed %v", i, results[i], w.id, w.confirmed)
		}
	}
}

func TestCustodyNeedsADevice(t *testing.T) {
	f := newFixture(t)
	if _, err := f.Confirm(context.Background(), "", nil); err == nil {
		t.Fatal("confirmed without an authenticated device")
	}
}

// write replaces a stored file's content, keeping it where the ledger says.
func write(f *fixture, rel, content string) {
	f.t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}
