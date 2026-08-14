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

// What this phone has sent and might later delete.
//
// A row here is a question waiting to be asked, not an answer: it holds the
// digest this phone computed over the bytes it sent, so the receiver can be
// asked to reproduce them. Deleting happens only after it does.

import type { DeletionCandidate } from '../core/deletion';
import type { ResourceType } from '../core/selection';
import { db } from './db';

type Row = {
  asset_key: string;
  asset_id: string;
  filename: string;
  stored_path: string;
  size: number;
  sha256: string;
  withheld: string | null;
};

/** Records a file that may become deletable, replacing any earlier answer. */
export async function recordCandidate(
  receiverId: string,
  candidate: DeletionCandidate,
): Promise<void> {
  await (
    await db()
  ).runAsync(
    `INSERT INTO pending_deletion
       (receiver_id, asset_key, asset_id, filename, stored_path, size, sha256, withheld, recorded_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT(receiver_id, asset_key) DO UPDATE SET
       asset_id    = excluded.asset_id,
       filename    = excluded.filename,
       stored_path = excluded.stored_path,
       size        = excluded.size,
       sha256      = excluded.sha256,
       withheld    = excluded.withheld,
       recorded_at = excluded.recorded_at`,
    receiverId,
    candidate.key,
    candidate.assetId,
    candidate.filename,
    candidate.storedPath,
    candidate.size,
    candidate.sha256,
    // NULL and "[]" mean different things and the difference decides whether
    // a photograph is deleted, so the round trip keeps them apart.
    candidate.withheld === undefined ? null : JSON.stringify(candidate.withheld),
    Date.now(),
  );
}

/** Everything waiting on this receiver's word. */
export async function pendingCandidates(receiverId: string): Promise<DeletionCandidate[]> {
  const rows = await (
    await db()
  ).getAllAsync<Row>(
    `SELECT asset_key, asset_id, filename, stored_path, size, sha256, withheld
       FROM pending_deletion WHERE receiver_id = ? ORDER BY recorded_at`,
    receiverId,
  );
  return rows.map(toCandidate);
}

/** The keys already waiting, so an unchanged file is not hashed again. */
export async function candidateKeys(receiverId: string): Promise<Set<string>> {
  const rows = await (
    await db()
  ).getAllAsync<{ asset_key: string }>(
    'SELECT asset_key FROM pending_deletion WHERE receiver_id = ?',
    receiverId,
  );
  return new Set(rows.map((row) => row.asset_key));
}

/** Forgets candidates, by ledger key. Called once their assets are gone. */
export async function clearCandidates(receiverId: string, keys: string[]): Promise<void> {
  if (keys.length === 0) return;

  const handle = await db();
  // Chunked: SQLite's variable limit is not large, and a library-sized delete
  // would otherwise fail at exactly the moment it succeeded.
  for (let i = 0; i < keys.length; i += 400) {
    const chunk = keys.slice(i, i + 400);
    await handle.runAsync(
      `DELETE FROM pending_deletion
        WHERE receiver_id = ? AND asset_key IN (${chunk.map(() => '?').join(',')})`,
      receiverId,
      ...chunk,
    );
  }
}

/** Forgets everything for a receiver: unpaired, or the setting turned off. */
export async function forgetCandidates(receiverId?: string): Promise<void> {
  const handle = await db();
  if (receiverId === undefined) {
    await handle.runAsync('DELETE FROM pending_deletion');
    return;
  }
  await handle.runAsync('DELETE FROM pending_deletion WHERE receiver_id = ?', receiverId);
}

function toCandidate(row: Row): DeletionCandidate {
  return {
    assetId: row.asset_id,
    key: row.asset_key,
    storedPath: row.stored_path,
    size: row.size,
    sha256: row.sha256,
    withheld: parseWithheld(row.withheld),
    filename: row.filename,
  };
}

/**
 * NULL, and anything that will not parse, both mean "not established" -- which
 * blocks deletion. A corrupted row must not read as "nothing was left behind".
 */
function parseWithheld(raw: string | null): ResourceType[] | undefined {
  if (raw === null) return undefined;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return undefined;
    if (!parsed.every((entry) => typeof entry === 'string')) return undefined;
    return parsed as ResourceType[];
  } catch {
    return undefined;
  }
}
