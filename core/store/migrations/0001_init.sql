-- Copyright 2026 Geda
-- SPDX-License-Identifier: Apache-2.0

-- Devices that have completed pairing with this receiver.
CREATE TABLE devices (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    platform    TEXT NOT NULL,

    -- SHA-256 of the peer's TLS SubjectPublicKeyInfo, base64. Recorded at
    -- pairing time and never updated: a change here is a hard failure that
    -- can only be resolved by re-pairing. See docs/PROTOCOL.md 3.5.
    spki_pin    TEXT NOT NULL,

    -- The bearer token is stored hashed so that a leaked database does not
    -- hand over working credentials.
    token_hash  TEXT NOT NULL,

    paired_at     TEXT NOT NULL,
    last_seen_at  TEXT,
    revoked_at    TEXT
) STRICT;

CREATE INDEX idx_devices_token ON devices(token_hash) WHERE revoked_at IS NULL;

-- Every file this receiver holds.
CREATE TABLE files (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id    TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,

    -- Full-file BLAKE3, hex. The authority for dedup and for confirming a
    -- transfer before any source file may be deleted.
    hash         TEXT NOT NULL,
    -- BLAKE3 of the first 1 MiB, hex. Cheap pre-filter for the dedup probe.
    head_hash    TEXT NOT NULL,
    size         INTEGER NOT NULL,

    -- The asset's capture date, not the transfer time. Also written to the
    -- stored file's mtime.
    captured_at  TEXT,
    original_name TEXT NOT NULL,

    -- The stored location, split so that collision probing is an indexed
    -- equality lookup rather than a filesystem glob. stored_path is
    -- dir/basename.ext and is what callers usually want.
    dir          TEXT NOT NULL,
    basename     TEXT NOT NULL,
    ext          TEXT NOT NULL,
    stored_path  TEXT NOT NULL UNIQUE,

    -- Non-null for members of a Live Photo or RAW+JPEG group.
    pair_id      TEXT,
    pair_role    TEXT CHECK (pair_role IN ('primary', 'secondary')),

    kind         TEXT NOT NULL CHECK (kind IN ('photo', 'video', 'file')),
    received_at  TEXT NOT NULL
) STRICT;

-- Serves the dedup probe: (size, captured_at, head_hash) -> have or not.
CREATE INDEX idx_files_dedup ON files(size, captured_at, head_hash);

-- Serves "does this device already have this exact content".
CREATE INDEX idx_files_hash ON files(device_id, hash);

-- Serves collision probing. The naming rule is that a basename is taken if
-- ANY extension uses it, so this must be looked up without the extension --
-- checking dir + basename + ext would let a Live Photo's .MOV collide with an
-- unrelated pair. See docs/PROTOCOL.md 5.1.
CREATE INDEX idx_files_basename ON files(dir, basename);

-- Basenames reserved per pair. The primary key is what makes concurrent
-- members of one pair safe: the loser of the insert race re-reads the
-- winner's basename, so neither side has to arrive first.
CREATE TABLE pair_basenames (
    device_id   TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    pair_id     TEXT NOT NULL,
    dir         TEXT NOT NULL,
    basename    TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (device_id, pair_id)
) STRICT;

-- One row per user-visible transfer session.
CREATE TABLE transfers (
    id           TEXT PRIMARY KEY,
    device_id    TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    direction    TEXT NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    status       TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'cancelled')),
    started_at   TEXT NOT NULL,
    finished_at  TEXT,
    files_total  INTEGER NOT NULL DEFAULT 0,
    files_done   INTEGER NOT NULL DEFAULT 0,
    bytes_total  INTEGER NOT NULL DEFAULT 0,
    bytes_done   INTEGER NOT NULL DEFAULT 0,
    error        TEXT
) STRICT;

CREATE INDEX idx_transfers_device ON transfers(device_id, started_at DESC);

-- Receiver configuration. Values are opaque strings; typing lives in Go.
CREATE TABLE settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
) STRICT;
