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

// The phone's own small database.
//
// One handle and one schema, opened once. Everything here is a cache or a
// preference: losing it costs bandwidth and a re-run, never a file. The
// receiver's ledger is the one that matters, and it is on the other side of
// the network.

import * as SQLite from 'expo-sqlite';

let database: SQLite.SQLiteDatabase | undefined;

const SCHEMA = `
  PRAGMA journal_mode = WAL;

  -- What this phone has sent, per receiver. Makes a second run over the same
  -- library fast: these assets are never opened, exported, or hashed again.
  CREATE TABLE IF NOT EXISTS sent (
    receiver_id TEXT NOT NULL,
    asset_key   TEXT NOT NULL,
    stored_path TEXT,
    size        INTEGER NOT NULL,
    sent_at     INTEGER NOT NULL,
    PRIMARY KEY (receiver_id, asset_key)
  );

  -- What this phone has collected from a receiver, and where it put it.
  --
  -- Keyed on the receiver's item id so that a download whose acknowledgement
  -- was lost is not collected and saved a second time: the desktop would
  -- still be offering it, and the user would get two copies of the same
  -- video in their camera roll.
  CREATE TABLE IF NOT EXISTS received (
    receiver_id TEXT NOT NULL,
    item_id     TEXT NOT NULL,
    filename    TEXT NOT NULL,
    size        INTEGER NOT NULL,
    sha256      TEXT NOT NULL,
    destination TEXT NOT NULL,
    saved_at    INTEGER NOT NULL,
    -- Null once the receiver has been told. A row with this still set is a
    -- file that arrived and was saved but whose acknowledgement never got
    -- through, and it is retried on the next check.
    unacked_at  INTEGER,
    PRIMARY KEY (receiver_id, item_id)
  );

  -- User preferences. Values are strings; typing lives in TypeScript.
  CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
  );
`;

export async function db(): Promise<SQLite.SQLiteDatabase> {
  if (database) return database;

  database = await SQLite.openDatabaseAsync('geda.db');
  await database.execAsync(SCHEMA);
  return database;
}
