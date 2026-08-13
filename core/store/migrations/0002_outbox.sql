-- Copyright 2026 Geda
-- SPDX-License-Identifier: Apache-2.0

-- Files this receiver is holding for a phone to come and collect.
--
-- A suspended iOS app cannot be pushed to (AGENTS.md 3.7), so the desktop
-- cannot send: it can only offer. This table is that offer. The phone asks
-- "anything for me?" when it comes to the foreground and works through what
-- it finds (docs/PROTOCOL.md 6).
--
-- The bytes are not copied here. Queueing a 2 GB archive must not cost 2 GB of
-- disk and a minute of copying, so a row points at the file where it already
-- lives. size and source_mtime are recorded when it is hashed, and re-checked
-- before it is served: a file edited after queueing fails the item rather than
-- sending a digest that no longer describes the content.
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    device_id    TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,

    -- Absolute path on this machine, chosen by the local user. It is never
    -- accepted over the network and never sent to the phone.
    source_path  TEXT NOT NULL,

    -- The basename, which is what the phone is offered. On the phone this is
    -- untrusted input and must be sanitised there before it becomes a path.
    filename     TEXT NOT NULL,

    size         INTEGER NOT NULL DEFAULT 0,
    source_mtime TEXT NOT NULL DEFAULT '',

    -- SHA-256, hex. Not BLAKE3: this digest exists to be re-computed on the
    -- phone, where CryptoKit does SHA-256 in hardware and a second hash
    -- implementation would be a correctness risk for no gain. See
    -- docs/DECISIONS.md.
    sha256       TEXT NOT NULL DEFAULT '',

    -- Decides where the file lands on the phone: photo and video may go to
    -- the Photo Library, file never does (AGENTS.md 3.7).
    kind         TEXT NOT NULL CHECK (kind IN ('photo', 'video', 'file')),

    -- The asset's capture date when there is one, so the Photo Library sorts
    -- it where the user expects rather than under today.
    captured_at  TEXT,

    -- pending  queued, not yet hashed -- not offered to the phone
    -- ready    hashed and on offer
    -- claimed  the phone has been handed the bytes at least once
    -- delivered the phone confirmed a hash match and acknowledged it
    -- failed   the source is gone, unreadable, or changed after queueing
    state        TEXT NOT NULL CHECK (state IN ('pending', 'ready', 'claimed', 'delivered', 'failed')),
    error        TEXT NOT NULL DEFAULT '',

    queued_at    TEXT NOT NULL,
    delivered_at TEXT
) STRICT;

-- Serves both "what is waiting for this phone" and the window's queue view.
CREATE INDEX idx_outbox_device ON outbox(device_id, state, queued_at);

-- Serves the hashing worker, which asks for the oldest unhashed row.
CREATE INDEX idx_outbox_pending ON outbox(queued_at) WHERE state = 'pending';
