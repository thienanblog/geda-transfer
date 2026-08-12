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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/hash"
	"github.com/geda/geda-transfer/core/naming"
	"github.com/geda/geda-transfer/core/storage"
	"github.com/geda/geda-transfer/core/store"
)

var captured = time.Date(2026, 7, 4, 15, 9, 3, 0, time.UTC)

type fixture struct {
	*storage.Store
	db   *store.DB
	root string
	t    *testing.T
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	root := filepath.Join(dir, "Photos")
	st, err := storage.New(db, root)
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{Store: st, db: db, root: root, t: t}
	f.pair("dev-1", "An's iPhone")
	return f
}

func (f *fixture) pair(id, name string) {
	f.t.Helper()
	_, err := f.db.SQL().ExecContext(f.t.Context(), `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at)
		VALUES (?, ?, 'ios', 'pin', 'hash', ?)`,
		id, name, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		f.t.Fatal(err)
	}
}

// temp writes content to a staging file, mimicking a finished upload.
func (f *fixture) temp(content string) string {
	f.t.Helper()
	path := filepath.Join(f.IncomingDir(), stagingName())
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

var stagingSeq atomic.Uint64

// stagingName is a unique name for a file in the incoming directory. Uploads
// in flight are identified by opaque ids, so the name carries no meaning.
func stagingName() string {
	return fmt.Sprintf("upload-%d", stagingSeq.Add(1))
}

func digestOf(t *testing.T, content string) hash.Digest {
	t.Helper()
	d, err := hash.Reader(context.Background(), bytes.NewReader([]byte(content)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *fixture) commit(in storage.Incoming, content string) storage.Committed {
	f.t.Helper()
	in.Digest = digestOf(f.t, content)
	if in.DeviceID == "" {
		in.DeviceID = "dev-1"
	}
	if in.Kind == "" {
		in.Kind = storage.KindPhoto
	}
	got, err := f.Commit(f.t.Context(), in, f.temp(content))
	if err != nil {
		f.t.Fatalf("Commit: %v", err)
	}
	return got
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestCommitPlacesFileAndRecordsIt(t *testing.T) {
	f := newFixture(t)

	got := f.commit(storage.Incoming{
		DeviceName:   "An's iPhone",
		OriginalName: "IMG_4021.HEIC",
		CapturedAt:   captured,
	}, "photo bytes")

	if want := "2026-07-04_150903_IMG_4021.HEIC"; got.Path != want {
		t.Fatalf("Path = %q, want %q", got.Path, want)
	}
	if got.Deduplicated {
		t.Error("a first upload must not report as deduplicated")
	}
	if content := f.read(got.Path); content != "photo bytes" {
		t.Errorf("stored content = %q", content)
	}

	var stored string
	err := f.db.SQL().QueryRowContext(t.Context(),
		`SELECT stored_path FROM files WHERE device_id = 'dev-1'`).Scan(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored != got.Path {
		t.Errorf("ledger has %q, disk has %q", stored, got.Path)
	}
}

// The stored file's mtime is the capture date, so a file browser sorts by when
// the photo was taken rather than when it was copied.
func TestCommitSetsMtimeToCaptureDate(t *testing.T) {
	f := newFixture(t)

	got := f.commit(storage.Incoming{
		OriginalName: "IMG_4021.HEIC",
		CapturedAt:   captured,
	}, "x")

	info, err := os.Stat(f.AbsPath(got))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().UTC().Equal(captured) {
		t.Errorf("mtime = %s, want %s", info.ModTime().UTC(), captured)
	}
}

// Identical content is skipped, not stored twice. This is the win on a re-run.
func TestIdenticalContentIsDeduplicated(t *testing.T) {
	f := newFixture(t)

	first := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, "same")
	second := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, "same")

	if !second.Deduplicated {
		t.Error("second upload of identical content was not deduplicated")
	}
	if second.Path != first.Path {
		t.Errorf("dedup pointed at %q, want the existing %q", second.Path, first.Path)
	}

	var n int
	if err := f.db.SQL().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("ledger has %d rows, want 1", n)
	}

	entries, err := os.ReadDir(f.root)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		if !e.IsDir() {
			files++
		}
	}
	if files != 1 {
		t.Errorf("%d files on disk, want 1", files)
	}
}

// Different content that renders to the same name gets a counter suffix.
func TestDifferentContentGetsCollisionCounter(t *testing.T) {
	f := newFixture(t)

	first := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, "one")
	second := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, "two")
	third := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, "three")

	if first.Path == second.Path || second.Path == third.Path || first.Path == third.Path {
		t.Fatalf("names collided: %q %q %q", first.Path, second.Path, third.Path)
	}
	if want := "2026-07-04_150903_IMG_1_1.HEIC"; second.Path != want {
		t.Errorf("second = %q, want %q", second.Path, want)
	}
	if want := "2026-07-04_150903_IMG_1_2.HEIC"; third.Path != want {
		t.Errorf("third = %q, want %q", third.Path, want)
	}

	if f.read(first.Path) != "one" || f.read(second.Path) != "two" || f.read(third.Path) != "three" {
		t.Error("collision handling mixed up file contents")
	}
}

// A Live Photo is one asset in two files. They must land on one basename, and
// neither member may be required to arrive first.
func TestLivePhotoPairSharesOneBasename(t *testing.T) {
	for _, order := range []struct {
		name  string
		first storage.Incoming
		next  storage.Incoming
	}{
		{
			name:  "primary first",
			first: storage.Incoming{OriginalName: "IMG_9.HEIC", PairID: "pair-a", PairRole: storage.RolePrimary},
			next:  storage.Incoming{OriginalName: "IMG_9.MOV", PairID: "pair-a", PairRole: storage.RoleSecondary, Kind: storage.KindVideo},
		},
		{
			name:  "secondary first",
			first: storage.Incoming{OriginalName: "IMG_9.MOV", PairID: "pair-a", PairRole: storage.RoleSecondary, Kind: storage.KindVideo},
			next:  storage.Incoming{OriginalName: "IMG_9.HEIC", PairID: "pair-a", PairRole: storage.RolePrimary},
		},
	} {
		t.Run(order.name, func(t *testing.T) {
			f := newFixture(t)

			order.first.CapturedAt = captured
			order.next.CapturedAt = captured

			a := f.commit(order.first, "still")
			b := f.commit(order.next, "video")

			baseA := strings.TrimSuffix(a.Path, filepath.Ext(a.Path))
			baseB := strings.TrimSuffix(b.Path, filepath.Ext(b.Path))
			if baseA != baseB {
				t.Errorf("pair split across basenames: %q and %q", a.Path, b.Path)
			}
			if filepath.Ext(a.Path) == filepath.Ext(b.Path) {
				t.Errorf("pair members share an extension: %q and %q", a.Path, b.Path)
			}
		})
	}
}

// The collision counter is allocated per pair. An unrelated file that would
// take the pair's basename must be pushed to _1, and the pair must stay
// together rather than having its second member pushed instead.
func TestUnrelatedFileCannotSplitAPair(t *testing.T) {
	f := newFixture(t)

	primary := f.commit(storage.Incoming{
		OriginalName: "IMG_9.HEIC", CapturedAt: captured,
		PairID: "pair-a", PairRole: storage.RolePrimary,
	}, "still")

	// A different asset that renders to the same name arrives in between.
	intruder := f.commit(storage.Incoming{
		OriginalName: "IMG_9.HEIC", CapturedAt: captured,
	}, "unrelated")

	secondary := f.commit(storage.Incoming{
		OriginalName: "IMG_9.MOV", CapturedAt: captured, Kind: storage.KindVideo,
		PairID: "pair-a", PairRole: storage.RoleSecondary,
	}, "video")

	basePrimary := strings.TrimSuffix(primary.Path, filepath.Ext(primary.Path))
	baseSecondary := strings.TrimSuffix(secondary.Path, filepath.Ext(secondary.Path))

	if basePrimary != baseSecondary {
		t.Errorf("intruder split the pair: primary %q, secondary %q", primary.Path, secondary.Path)
	}
	if intruder.Path == primary.Path {
		t.Errorf("intruder took the pair's name: %q", intruder.Path)
	}
}

func TestTemplateComesFromSettings(t *testing.T) {
	f := newFixture(t)

	if err := f.db.SetSetting(t.Context(), storage.SettingTemplate,
		"{yyyy}/{MM}/{device}/{original_name}.{ext}"); err != nil {
		t.Fatal(err)
	}

	got := f.commit(storage.Incoming{
		DeviceName:   "An's iPhone",
		OriginalName: "IMG_4021.HEIC",
		CapturedAt:   captured,
	}, "x")

	if want := "2026/07/An's iPhone/IMG_4021.HEIC"; got.Path != want {
		t.Errorf("Path = %q, want %q", got.Path, want)
	}
	if f.read(got.Path) != "x" {
		t.Error("file is not where the ledger says it is")
	}
}

func TestBlankTemplateFallsBackToDefault(t *testing.T) {
	f := newFixture(t)

	if err := f.db.SetSetting(t.Context(), storage.SettingTemplate, "   "); err != nil {
		t.Fatal(err)
	}
	tmpl, err := f.Template(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != naming.Default {
		t.Errorf("template = %q, want the default", tmpl)
	}
}

// A file the user dropped into the destination themselves is not in the
// ledger. Committing over it would destroy data.
func TestExistingUnknownFileIsNotOverwritten(t *testing.T) {
	f := newFixture(t)

	squatter := filepath.Join(f.root, "2026-07-04_150903_IMG_1.HEIC")
	if err := os.WriteFile(squatter, []byte("the user's own file"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := f.commit(storage.Incoming{OriginalName: "IMG_1.HEIC", CapturedAt: captured}, "incoming")

	if b, err := os.ReadFile(squatter); err != nil || string(b) != "the user's own file" {
		t.Fatalf("pre-existing file was destroyed: %q, %v", b, err)
	}
	if got.Path == "2026-07-04_150903_IMG_1.HEIC" {
		t.Errorf("committed on top of an unknown file at %q", got.Path)
	}
}

func TestConcurrentCommitsNeverShareAName(t *testing.T) {
	f := newFixture(t)

	const n = 16
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			content := "content-" + string(rune('a'+i))
			in := storage.Incoming{
				DeviceID:     "dev-1",
				OriginalName: "IMG_1.HEIC",
				CapturedAt:   captured,
				Kind:         storage.KindPhoto,
				Digest:       digestOf(t, content),
			}
			got, err := f.Commit(t.Context(), in, f.temp(content))
			paths[i], errs[i] = got.Path, err
		}()
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		if seen[paths[i]] {
			t.Fatalf("two commits produced %q", paths[i])
		}
		seen[paths[i]] = true
	}
	if len(seen) != n {
		t.Errorf("%d distinct names for %d files", len(seen), n)
	}
}

func TestCommitRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	d := digestOf(t, "x")

	cases := map[string]storage.Incoming{
		"no device": {OriginalName: "a.jpg", Kind: storage.KindPhoto, Digest: d},
		"no digest": {DeviceID: "dev-1", OriginalName: "a.jpg", Kind: storage.KindPhoto},
		"bad kind":  {DeviceID: "dev-1", OriginalName: "a.jpg", Kind: "nonsense", Digest: d},
	}

	for name, in := range cases {
		if _, err := f.Commit(t.Context(), in, f.temp("x")); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// A template that renders outside the destination must not be able to write
// there, even though naming already sanitises: storage is the last line.
func TestCommittedPathStaysInsideRoot(t *testing.T) {
	f := newFixture(t)

	got := f.commit(storage.Incoming{
		OriginalName: "../../../etc/passwd",
		CapturedAt:   captured,
	}, "x")

	abs, err := filepath.Abs(f.AbsPath(got))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		t.Fatalf("committed to %q, which is outside %q", abs, root)
	}
}

// Two members of one pair claiming the same extension cannot both hold the
// pair's name. Keeping the bytes matters more than keeping the grouping, so
// the transfer must still succeed.
func TestPairMembersWithTheSameExtensionStillBothLand(t *testing.T) {
	f := newFixture(t)

	first := f.commit(storage.Incoming{
		OriginalName: "IMG_9.HEIC", CapturedAt: captured,
		PairID: "pair-a", PairRole: storage.RolePrimary,
	}, "one")

	second := f.commit(storage.Incoming{
		OriginalName: "IMG_9.HEIC", CapturedAt: captured,
		PairID: "pair-a", PairRole: storage.RoleSecondary,
	}, "two")

	if first.Path == second.Path {
		t.Fatalf("both members claimed %q", first.Path)
	}
	if f.read(first.Path) != "one" || f.read(second.Path) != "two" {
		t.Error("contents were mixed up")
	}
}
