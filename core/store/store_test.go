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

package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/geda/geda-transfer/core/store"
)

func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// pairDevice inserts the device row that files and transfers hang off, so
// tests exercise the real foreign keys rather than working around them.
func pairDevice(t *testing.T, db *store.DB, id string) {
	t.Helper()
	_, err := db.SQL().ExecContext(t.Context(), `
		INSERT INTO devices (id, name, platform, spki_pin, token_hash, paired_at)
		VALUES (?, ?, 'ios', 'pin', 'tokenhash', ?)`,
		id, "Test "+id, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("pair device: %v", err)
	}
}

func insertFile(t *testing.T, db *store.DB, deviceID, dir, basename, ext string) error {
	t.Helper()
	_, err := db.SQL().ExecContext(t.Context(), `
		INSERT INTO files (device_id, hash, head_hash, size, original_name,
		                   dir, basename, ext, stored_path, kind, received_at)
		VALUES (?, 'fullhash', 'headhash', 1, ?, ?, ?, ?, ?, 'photo', ?)`,
		deviceID, basename+"."+ext, dir, basename, ext,
		dir+"/"+basename+"."+ext, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func TestOpenAppliesSchema(t *testing.T) {
	db := open(t)

	v, err := db.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if v < 1 {
		t.Fatalf("schema version = %d, want at least 1", v)
	}

	for _, table := range []string{
		"devices", "files", "pair_basenames", "transfers", "settings", "schema_migrations",
	} {
		var name string
		err := db.SQL().QueryRowContext(t.Context(),
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

// Reopening must be a no-op, not a re-application of the schema.
func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")

	first, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetSetting(t.Context(), "destination", "/Volumes/Photos"); err != nil {
		t.Fatal(err)
	}
	wantVersion, err := first.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	if got, _ := second.SchemaVersion(t.Context()); got != wantVersion {
		t.Errorf("schema version = %d after reopen, want %d", got, wantVersion)
	}

	// Data survived, so the schema was not recreated.
	got, err := second.Setting(t.Context(), "destination")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Volumes/Photos" {
		t.Errorf("destination = %q, want /Volumes/Photos", got)
	}

	var applied int
	err = second.SQL().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 1`).Scan(&applied)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Errorf("migration 1 recorded %d times, want 1", applied)
	}
}

func TestSettingRoundTrip(t *testing.T) {
	db := open(t)

	if _, err := db.Setting(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	if err := db.SetSetting(t.Context(), "naming_template", "{yyyy}/{MM}"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Setting(t.Context(), "naming_template"); got != "{yyyy}/{MM}" {
		t.Errorf("got %q", got)
	}

	// Overwrite, not a second row.
	if err := db.SetSetting(t.Context(), "naming_template", "{yyyy}"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Setting(t.Context(), "naming_template"); got != "{yyyy}" {
		t.Errorf("after overwrite got %q, want {yyyy}", got)
	}

	// An empty value is a real value, distinct from unset.
	if err := db.SetSetting(t.Context(), "empty", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := db.Setting(t.Context(), "empty"); err != nil || got != "" {
		t.Errorf("empty setting: got %q err %v", got, err)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	db := open(t)

	// Without ON, SQLite silently accepts orphans and the ledger rots.
	err := insertFile(t, db, "ghost-device", "2026/07", "IMG_0001", "HEIC")
	if err == nil {
		t.Fatal("inserted a file for a device that does not exist")
	}
}

func TestDeletingADeviceCascades(t *testing.T) {
	db := open(t)
	pairDevice(t, db, "dev-1")

	if err := insertFile(t, db, "dev-1", "2026/07", "IMG_0001", "HEIC"); err != nil {
		t.Fatal(err)
	}
	_, err := db.SQL().ExecContext(t.Context(), `
		INSERT INTO pair_basenames (device_id, pair_id, dir, basename, created_at)
		VALUES ('dev-1', 'pair-a', '2026/07', 'IMG_0001', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.SQL().ExecContext(t.Context(), `DELETE FROM devices WHERE id = 'dev-1'`); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"files", "pair_basenames"} {
		var n int
		if err := db.SQL().QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("unpairing left %d rows in %s", n, table)
		}
	}
}

func TestStoredPathIsUnique(t *testing.T) {
	db := open(t)
	pairDevice(t, db, "dev-1")

	if err := insertFile(t, db, "dev-1", "2026/07", "IMG_0001", "HEIC"); err != nil {
		t.Fatal(err)
	}
	if err := insertFile(t, db, "dev-1", "2026/07", "IMG_0001", "HEIC"); err == nil {
		t.Fatal("two files claimed the same stored_path")
	}
}

// The collision rule is per-basename across all extensions: a Live Photo's
// .MOV must not be able to land on an unrelated pair's basename. The index
// exists to make that probe cheap, so assert the query it serves.
func TestBasenameProbeIgnoresExtension(t *testing.T) {
	db := open(t)
	pairDevice(t, db, "dev-1")

	if err := insertFile(t, db, "dev-1", "2026/07", "IMG_0001", "HEIC"); err != nil {
		t.Fatal(err)
	}

	var taken int
	err := db.SQL().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM files WHERE dir = ? AND basename = ?`,
		"2026/07", "IMG_0001").Scan(&taken)
	if err != nil {
		t.Fatal(err)
	}
	if taken != 1 {
		t.Errorf("basename probe found %d, want 1 — a .MOV would collide", taken)
	}
}

// The pair primary key is what removes any ordering requirement between a
// Live Photo's two members. Whichever arrives second must read back the
// basename the first one reserved.
func TestPairBasenameReservationIsRaceSafe(t *testing.T) {
	db := open(t)
	pairDevice(t, db, "dev-1")

	reserve := func(basename string) (string, error) {
		tx, err := db.SQL().BeginTx(t.Context(), nil)
		if err != nil {
			return "", err
		}
		defer tx.Rollback()

		var existing string
		err = tx.QueryRowContext(t.Context(),
			`SELECT basename FROM pair_basenames WHERE device_id = ? AND pair_id = ?`,
			"dev-1", "pair-a").Scan(&existing)
		if err == nil {
			return existing, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}

		_, err = tx.ExecContext(t.Context(), `
			INSERT INTO pair_basenames (device_id, pair_id, dir, basename, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			"dev-1", "pair-a", "2026/07", basename, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return "", err
		}
		return basename, tx.Commit()
	}

	var wg sync.WaitGroup
	results := make([]string, 2)
	errs := make([]error, 2)
	candidates := []string{"IMG_0001", "IMG_0002"}

	for i := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = reserve(candidates[i])
		}()
	}
	wg.Wait()

	var winner string
	for i := range results {
		if errs[i] != nil {
			// A losing writer may hit the unique constraint; it must then be
			// able to read the winner's value.
			got, err := reserve("")
			if err != nil {
				t.Fatalf("loser could not read the reservation: %v", err)
			}
			results[i] = got
			continue
		}
		if winner == "" {
			winner = results[i]
		}
	}

	if results[0] != results[1] {
		t.Errorf("pair members got different basenames: %q and %q", results[0], results[1])
	}

	var rows int
	if err := db.SQL().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM pair_basenames`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("pair_basenames has %d rows, want 1", rows)
	}
}

func TestCheckConstraintsRejectBadEnums(t *testing.T) {
	db := open(t)
	pairDevice(t, db, "dev-1")

	if err := insertFile(t, db, "dev-1", "2026/07", "IMG_0001", "HEIC"); err != nil {
		t.Fatal(err)
	}

	_, err := db.SQL().ExecContext(t.Context(), `UPDATE files SET kind = 'nonsense'`)
	if err == nil {
		t.Error("accepted an invalid kind")
	}

	_, err = db.SQL().ExecContext(t.Context(), `
		INSERT INTO transfers (id, device_id, direction, status, started_at)
		VALUES ('t1', 'dev-1', 'sideways', 'running', '2026-08-12T00:00:00Z')`)
	if err == nil {
		t.Error("accepted an invalid direction")
	}
}

func TestOpenCreatesMissingDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "ledger.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open in a missing directory: %v", err)
	}
	db.Close()
}

func TestOpenInMemory(t *testing.T) {
	db, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The schema must be visible on whichever pooled connection is used next,
	// which is what pins the pool to one connection.
	for range 5 {
		if _, err := db.SchemaVersion(context.Background()); err != nil {
			t.Fatalf("in-memory schema vanished: %v", err)
		}
	}
}

// The desktop app and gedad can be pointed at the same ledger and started
// together. With a deferred transaction both would take a read lock for the
// version check and then deadlock on the upgrade, failing with
// SQLITE_BUSY_SNAPSHOT — which busy_timeout cannot retry away.
func TestConcurrentOpenersBothMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")

	const openers = 6
	var wg sync.WaitGroup
	errs := make([]error, openers)
	dbs := make([]*store.DB, openers)

	start := make(chan struct{})
	for i := range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			dbs[i], errs[i] = store.Open(context.Background(), path)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
			continue
		}
		t.Cleanup(func() { dbs[i].Close() })
	}
	if t.Failed() {
		t.FailNow()
	}

	// Each migration must have been applied exactly once regardless of how
	// many processes raced to do it. Counting rows against a fixed number
	// would only be asserting how many migrations exist today, so the check
	// is that no version was recorded twice.
	var rows, versions int
	err := dbs[0].SQL().QueryRowContext(t.Context(),
		`SELECT COUNT(*), COUNT(DISTINCT version) FROM schema_migrations`).Scan(&rows, &versions)
	if err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("no migrations were applied at all")
	}
	if rows != versions {
		t.Errorf("schema_migrations has %d rows for %d versions; one was applied twice", rows, versions)
	}

	latest, err := dbs[0].SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if latest != versions {
		t.Errorf("schema version is %d after applying %d migrations", latest, versions)
	}
}

// WAL is what lets the receiver keep writing ledger rows while the UI reads
// from the same database. It is set outside the DSN, so assert it took effect.
func TestWALIsEnabled(t *testing.T) {
	db := open(t)

	var mode string
	if err := db.SQL().QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestForeignKeysPragmaAppliesToEveryPooledConnection(t *testing.T) {
	db := open(t)

	// Force the pool to hand out several distinct connections, since a pragma
	// applied to only the first one would still pass a single-query test.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var on int
			if err := db.SQL().QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&on); err != nil {
				t.Errorf("read foreign_keys: %v", err)
				return
			}
			if on != 1 {
				t.Errorf("foreign_keys = %d on a pooled connection, want 1", on)
			}
		}()
	}
	wg.Wait()
}
