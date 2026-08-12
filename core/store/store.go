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

// Package store is the receiver's ledger: which devices are paired, which
// files have already arrived, which basenames are reserved, and what the user
// has configured.
//
// The database is SQLite through modernc.org/sqlite, a pure-Go driver. That
// choice is deliberate: no cgo means the desktop app, the CLI, and the Docker
// image all cross-compile to macOS, Windows, Linux, amd64, and arm64 from one
// machine with no toolchain per target.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned by lookups that found nothing.
var ErrNotFound = errors.New("not found")

// DB is a handle to the ledger.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the ledger at path and brings its schema up
// to date. Pass ":memory:" for an ephemeral database.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := path
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("create ledger directory: %w", err)
			}
		}
		// These pragmas are per-connection, so they go in the DSN and apply to
		// every connection the pool hands out.
		//
		// journal_mode is deliberately NOT here. Switching a database to WAL
		// needs an exclusive lock, and SQLite does not run the busy handler
		// for that conversion: with several processes opening the same ledger
		// at once, some connections fail outright with SQLITE_BUSY during
		// setup, before any query runs. WAL is a persistent property of the
		// file, so it is set once by enableWAL below instead.
		dsn = path + "?" + strings.Join([]string{
			"_pragma=busy_timeout(5000)",
			"_pragma=synchronous(NORMAL)",
			"_pragma=foreign_keys(ON)",
		}, "&")
	} else {
		dsn = ":memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}

	if path == ":memory:" {
		// Every connection to ":memory:" gets its own private database, so the
		// pool must be held to exactly one connection or the schema appears to
		// vanish at random.
		sqlDB.SetMaxOpenConns(1)
	}

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("open ledger: %w", err)
	}

	if path != ":memory:" {
		if err := enableWAL(ctx, sqlDB); err != nil {
			sqlDB.Close()
			return nil, err
		}
	}

	db := &DB{sql: sqlDB}
	if err := db.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// enableWAL puts the database into write-ahead logging, retrying while another
// process holds the exclusive lock it needs.
//
// WAL matters because it lets the receiver keep writing ledger rows while the
// UI reads from it. The retry exists because the conversion cannot wait on its
// own: SQLite returns SQLITE_BUSY immediately rather than invoking the busy
// handler, so contention has to be handled here. Once any process succeeds the
// setting is persistent, and every later opener simply observes "wal".
func enableWAL(ctx context.Context, sqlDB *sql.DB) error {
	const attempts = 50

	var last error
	for attempt := range attempts {
		var mode string
		err := sqlDB.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode)
		switch {
		case err != nil:
			last = err
		case strings.EqualFold(mode, "wal"):
			return nil
		default:
			last = fmt.Errorf("journal_mode is %q", mode)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Millisecond):
		}
	}
	return fmt.Errorf("enable WAL: %w", last)
}

// Close releases the underlying handle.
func (db *DB) Close() error { return db.sql.Close() }

// SQL exposes the underlying handle for packages that build on the ledger.
func (db *DB) SQL() *sql.DB { return db.sql }

// migration is one numbered schema step, loaded from migrations/.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads and orders the embedded migrations. File names are
// NNNN_description.sql; the numeric prefix is the version.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	out := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}

		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: want NNNN_description.sql", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version prefix: %w", name, err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", prev, name, version)
		}
		seen[version] = name

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}

		out = append(out, migration{version: version, name: name, sql: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// migrate applies every migration newer than the recorded version. It is safe
// to call on an up-to-date database, and safe when two processes start against
// the same ledger at once -- which happens whenever the desktop app and gedad
// point at the same directory.
func (db *DB) migrate(ctx context.Context) error {
	const createVersions = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		) STRICT`
	if _, err := db.sql.ExecContext(ctx, createVersions); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if err := db.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
	}
	return nil
}

func (db *DB) applyMigration(ctx context.Context, m migration) error {
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// BEGIN IMMEDIATE, not a deferred transaction. A deferred one takes a read
	// lock for the version check and only tries to upgrade to a write lock at
	// the first write. Two processes migrating at once would both hold read
	// locks and one would fail with SQLITE_BUSY_SNAPSHOT -- which busy_timeout
	// does not retry, because there is no wait that can resolve it. Taking the
	// write lock up front turns that race into a plain wait.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			// The rollback must still run when ctx is the reason we are here.
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()

	var applied int
	err = conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&applied)
	if err != nil {
		return err
	}
	if applied > 0 {
		// Another process got here first. The deferred rollback releases the
		// write lock.
		return nil
	}

	if _, err := conn.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, nowRFC3339())
	if err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}

// SchemaVersion reports the highest applied migration, or 0 on a fresh
// database.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := db.sql.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// Setting reads a configuration value. It returns ErrNotFound if the key has
// never been set, so callers can distinguish "unset" from "set to empty".
func (db *DB) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("setting %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("read setting %q: %w", key, err)
	}
	return value, nil
}

// SetSetting writes a configuration value, replacing any previous one.
func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, nowRFC3339())
	if err != nil {
		return fmt.Errorf("write setting %q: %w", key, err)
	}
	return nil
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }
