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

package formats

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/store"
)

type queueFixture struct {
	*Queue
	db   *store.DB
	root string
	t    *testing.T
}

func newQueueFixture(t *testing.T, tools Tools) *queueFixture {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.SQL().ExecContext(t.Context(), `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at)
		VALUES ('dev-1', 'An''s iPhone', 'ios', 'pin', 'hash', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(dir, "Photos")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	q, err := NewQueue(QueueConfig{
		DB:        db,
		Root:      root,
		Converter: NewConverter(tools),
		Workers:   2,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &queueFixture{Queue: q, db: db, root: root, t: t}
}

// receive puts a file in the destination and records it, the way a finished
// upload does.
func (f *queueFixture) receive(name, body string) (int64, string) {
	f.t.Helper()

	abs := filepath.Join(f.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		f.t.Fatal(err)
	}
	write(f.t, abs, body)

	dir, file := filepath.Split(name)
	ext := filepath.Ext(file)
	res, err := f.db.SQL().ExecContext(f.t.Context(), `
		INSERT INTO files (device_id, hash, head_hash, size, original_name,
		                   dir, basename, ext, stored_path, kind, received_at)
		VALUES ('dev-1', ?, 'head', ?, ?, ?, ?, ?, ?, 'photo', ?)`,
		"hash-"+name, len(body), file,
		trimSlash(dir), file[:len(file)-len(ext)], trimDot(ext), name,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		f.t.Fatal(err)
	}
	return id, abs
}

func (f *queueFixture) item(id int64) Item {
	f.t.Helper()
	item, err := f.scanOne(f.t.Context(), `SELECT `+columns+` FROM conversions WHERE file_id = ?`, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return item
}

func (f *queueFixture) originalRemoved(fileID int64) bool {
	f.t.Helper()
	var at sql.NullString
	err := f.db.SQL().QueryRowContext(f.t.Context(),
		`SELECT original_removed_at FROM files WHERE id = ?`, fileID).Scan(&at)
	if err != nil {
		f.t.Fatal(err)
	}
	return at.Valid
}

func trimSlash(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '/' || s[len(s)-1] == filepath.Separator) {
		s = s[:len(s)-1]
	}
	return filepath.ToSlash(s)
}

func trimDot(ext string) string {
	if len(ext) > 0 && ext[0] == '.' {
		return ext[1:]
	}
	return ext
}

func TestQueueSidecarKeepsTheOriginal(t *testing.T) {
	f := newQueueFixture(t, fakeTools(t, fakeOK))
	fileID, abs := f.receive("2026/IMG_0042.HEIC", "heic bytes")

	if err := Enqueue(t.Context(), f.db, Request{
		FileID: fileID, DeviceID: "dev-1",
		SourcePath: "2026/IMG_0042.HEIC", Class: ClassHEIC, Action: ActionSidecar,
	}); err != nil {
		t.Fatal(err)
	}

	if n, err := f.ConvertPending(t.Context()); err != nil || n != 1 {
		t.Fatalf("ConvertPending = %d, %v; want 1, nil", n, err)
	}

	item := f.item(fileID)
	if item.State != StateDone {
		t.Fatalf("state is %q (%s)", item.State, item.Error)
	}
	if item.OutputPath != "2026/IMG_0042.jpg" {
		t.Fatalf("output is %q, want 2026/IMG_0042.jpg -- destination-relative, forward slashes", item.OutputPath)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("a sidecar removed the original: %v", err)
	}
	if f.originalRemoved(fileID) {
		t.Fatal("a sidecar marked the original as removed")
	}
}

func TestQueueReplaceRemovesTheOriginalAndSaysSo(t *testing.T) {
	f := newQueueFixture(t, fakeTools(t, fakeOK))
	fileID, abs := f.receive("IMG_0042.HEIC", "heic bytes")

	if err := Enqueue(t.Context(), f.db, Request{
		FileID: fileID, DeviceID: "dev-1",
		SourcePath: "IMG_0042.HEIC", Class: ClassHEIC, Action: ActionReplace,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ConvertPending(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("space-saving kept the original: %v", err)
	}

	// The ledger still holds the hash of bytes this machine no longer has, so
	// it must record that it can no longer prove it holds them. P9's
	// delete-after-transfer reads exactly this column.
	if !f.originalRemoved(fileID) {
		t.Fatal("the original was deleted without the ledger recording it; " +
			"the receiver would still authorise deleting the phone's copy")
	}
}

// A conversion that cannot run is not a failed transfer. The file is already
// stored, and the row has to say why nothing happened rather than going red.
func TestQueueSkipsWhenNoToolIsInstalled(t *testing.T) {
	f := newQueueFixture(t, noTools(t))
	fileID, abs := f.receive("IMG_0042.HEIC", "heic bytes")

	if err := Enqueue(t.Context(), f.db, Request{
		FileID: fileID, DeviceID: "dev-1",
		SourcePath: "IMG_0042.HEIC", Class: ClassHEIC, Action: ActionReplace,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ConvertPending(t.Context()); err != nil {
		t.Fatal(err)
	}

	item := f.item(fileID)
	if item.State != StateSkipped {
		t.Fatalf("state is %q, want %q", item.State, StateSkipped)
	}
	if item.Note == "" {
		t.Fatal("nothing was converted and the row does not say why")
	}
	// Above all: a space-saving policy on a machine with no converter must
	// not delete anything.
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("the original was removed without a converted copy to show for it: %v", err)
	}
	if f.originalRemoved(fileID) {
		t.Fatal("the ledger says the original was removed; it was not")
	}
}

func TestQueueRecordsAFailure(t *testing.T) {
	f := newQueueFixture(t, fakeTools(t, fakeFail))
	fileID, abs := f.receive("IMG_0042.HEIC", "heic bytes")

	if err := Enqueue(t.Context(), f.db, Request{
		FileID: fileID, DeviceID: "dev-1",
		SourcePath: "IMG_0042.HEIC", Class: ClassHEIC, Action: ActionReplace,
	}); err != nil {
		t.Fatal(err)
	}
	// A tool that refuses one file must not stop the queue, so this returns
	// no error at all.
	if _, err := f.ConvertPending(t.Context()); err != nil {
		t.Fatalf("one bad file stopped the queue: %v", err)
	}

	item := f.item(fileID)
	if item.State != StateFailed || item.Error == "" {
		t.Fatalf("state is %q, error %q", item.State, item.Error)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("a failed conversion removed the original: %v", err)
	}
}

func TestQueueFailsWhenTheFileHasGone(t *testing.T) {
	f := newQueueFixture(t, fakeTools(t, fakeOK))
	fileID, abs := f.receive("IMG_0042.HEIC", "heic bytes")
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}

	if err := Enqueue(t.Context(), f.db, Request{
		FileID: fileID, DeviceID: "dev-1",
		SourcePath: "IMG_0042.HEIC", Class: ClassHEIC, Action: ActionSidecar,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ConvertPending(t.Context()); err != nil {
		t.Fatal(err)
	}

	if item := f.item(fileID); item.State != StateFailed {
		t.Fatalf("state is %q, want %q", item.State, StateFailed)
	}
}

// A receiver killed mid-conversion leaves rows claimed by a process that no
// longer exists. Nothing else would ever pick them up.
func TestQueueRequeuesInterruptedRows(t *testing.T) {
	f := newQueueFixture(t, fakeTools(t, fakeOK))
	fileID, _ := f.receive("IMG_0042.HEIC", "heic bytes")

	if err := Enqueue(t.Context(), f.db, Request{
		FileID: fileID, DeviceID: "dev-1",
		SourcePath: "IMG_0042.HEIC", Class: ClassHEIC, Action: ActionSidecar,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SQL().ExecContext(t.Context(),
		`UPDATE conversions SET state = 'running'`); err != nil {
		t.Fatal(err)
	}

	if n, err := f.ConvertPending(t.Context()); err != nil || n != 0 {
		t.Fatalf("a claimed row was picked up without recovery: %d, %v", n, err)
	}
	if n, err := f.recover(t.Context()); err != nil || n != 1 {
		t.Fatalf("recover = %d, %v; want 1, nil", n, err)
	}
	if n, err := f.ConvertPending(t.Context()); err != nil || n != 1 {
		t.Fatalf("ConvertPending after recovery = %d, %v", n, err)
	}
}

// Two workers on one row would write two sidecars, and one of them would take
// a name the other had already claimed.
func TestQueueConvertsEachFileOnce(t *testing.T) {
	f := newQueueFixture(t, fakeTools(t, fakeOK))

	const files = 12
	for i := range files {
		name := fmt.Sprintf("IMG_%04d.HEIC", i)
		fileID, _ := f.receive(name, "heic bytes")
		if err := Enqueue(t.Context(), f.db, Request{
			FileID: fileID, DeviceID: "dev-1",
			SourcePath: name, Class: ClassHEIC, Action: ActionSidecar,
		}); err != nil {
			t.Fatal(err)
		}
		// Enqueue is INSERT OR IGNORE on file_id: a client that retried, or a
		// receiver that swept twice, must not queue the same file again.
		if err := Enqueue(t.Context(), f.db, Request{
			FileID: fileID, DeviceID: "dev-1",
			SourcePath: name, Class: ClassHEIC, Action: ActionSidecar,
		}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := f.ConvertPending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != files {
		t.Fatalf("converted %d rows, want %d", n, files)
	}

	entries := readDir(t, f.root)
	if len(entries) != files*2 {
		t.Fatalf("the destination holds %d files, want %d originals and %d sidecars: %v",
			len(entries), files, files, entries)
	}

	if pending, err := f.Pending(t.Context()); err != nil || pending != 0 {
		t.Fatalf("Pending = %d, %v; want 0", pending, err)
	}
}

func TestEnqueueRefusesWorkThatIsNotWork(t *testing.T) {
	f := newQueueFixture(t, fakeTools(t, fakeOK))
	fileID, _ := f.receive("IMG_0042.HEIC", "heic bytes")

	if err := Enqueue(t.Context(), f.db, Request{
		FileID: fileID, DeviceID: "dev-1",
		SourcePath: "IMG_0042.HEIC", Class: ClassHEIC, Action: ActionKeep,
	}); err == nil {
		t.Fatal("a keep was queued; the default preset would fill the table with nothing")
	}
}

func TestRecentIsNewestFirst(t *testing.T) {
	f := newQueueFixture(t, fakeTools(t, fakeOK))
	for i := range 3 {
		name := fmt.Sprintf("IMG_%04d.HEIC", i)
		fileID, _ := f.receive(name, "heic bytes")
		if err := Enqueue(t.Context(), f.db, Request{
			FileID: fileID, DeviceID: "dev-1",
			SourcePath: name, Class: ClassHEIC, Action: ActionSidecar,
		}); err != nil {
			t.Fatal(err)
		}
	}

	items, err := f.Recent(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("Recent returned %d rows", len(items))
	}
	for i := 1; i < len(items); i++ {
		if items[i].ID > items[i-1].ID {
			t.Fatal("Recent is not newest first")
		}
	}
}
