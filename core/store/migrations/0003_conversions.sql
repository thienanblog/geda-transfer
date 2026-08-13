-- Copyright 2026 Geda
-- SPDX-License-Identifier: Apache-2.0

-- Conversions the receiver owes on files it has already stored.
--
-- The phone sends originals and never transcodes (AGENTS.md 3.3), so every
-- conversion happens here, after the bytes are safe. That ordering is the
-- point: a transfer is never blocked, slowed, or failed by a converter, and a
-- machine with no ffmpeg on it receives exactly as well as one with.
--
-- A row is created only when the output policy asks for one, so a receiver on
-- the default Original preset never writes to this table at all.
CREATE TABLE conversions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,

    -- The received file. ON DELETE CASCADE because a conversion of a file
    -- that is no longer in the ledger is work with nothing to attach to.
    file_id     INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    device_id   TEXT NOT NULL,

    -- Destination-relative, forward slashes, as files.stored_path is.
    source_path TEXT NOT NULL,

    class       TEXT NOT NULL CHECK (class IN ('heic', 'video', 'raw', 'other')),

    -- Only the two actions that do work reach this table; 'keep' means no row.
    action      TEXT NOT NULL CHECK (action IN ('sidecar', 'replace')),

    -- pending  waiting for a worker
    -- running  a worker has it; reset to pending at startup, because a
    --          receiver killed mid-convert would otherwise never retry
    -- done     the converted file exists
    -- skipped  nothing to do -- already H.264, or no converter installed
    -- failed   the tool refused the file
    state       TEXT NOT NULL CHECK (state IN ('pending', 'running', 'done', 'skipped', 'failed')),

    output_path TEXT NOT NULL DEFAULT '',
    output_size INTEGER NOT NULL DEFAULT 0,
    tool        TEXT NOT NULL DEFAULT '',

    -- Why the outcome differs from what the preset asked for: a replace that
    -- was downgraded to a sidecar, or a skip that had no converter behind it.
    -- The user asked for something and did not get it, so the reason has to
    -- reach the screen rather than being inferred from a state name.
    note        TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',

    queued_at   TEXT NOT NULL,
    finished_at TEXT,

    -- One conversion per received file. A file re-offered by a client that
    -- retried is deduplicated upstream, and two workers racing on one file
    -- would write two sidecars.
    UNIQUE (file_id)
) STRICT;

-- Serves the worker, which asks for the oldest unconverted row.
CREATE INDEX idx_conversions_pending ON conversions(queued_at) WHERE state = 'pending';

-- Serves the window's per-device history view.
CREATE INDEX idx_conversions_device ON conversions(device_id, queued_at);

-- Set when a space-saving conversion removed the received original.
--
-- files.hash still describes the bytes that arrived, and it must: it is what
-- the sending phone is told to compare against. But those bytes are no longer
-- on this disk, so the receiver can no longer prove it holds them -- and
-- delete-after-transfer (P9) may only ever act on a proof. A file with this
-- column set is therefore never eligible to authorise deleting the phone's
-- copy, whatever the hashes say.
ALTER TABLE files ADD COLUMN original_removed_at TEXT;
