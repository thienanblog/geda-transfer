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

// What this phone has already sent, per receiver.
//
// This is what makes a second run over the same library fast: the assets that
// went last time are never opened, exported, or hashed again. The receiver
// deduplicates by full hash as well, so a missing entry costs bandwidth, never
// correctness -- which is the right way round for a cache.
//
// The protocol's dedup probe (docs/PROTOCOL.md §4) is the cross-device version
// of this and needs a BLAKE3 of the first megabyte, which cannot be computed
// without moving those bytes through the bridge. It arrives with the native
// hashing in a later phase; until then this local record covers the case that
// matters, which is the same phone sending to the same receiver again.

import type { Asset } from '../core/types';
import { ledgerKey } from '../core/plan';
import { db } from './db';

/** The keys this receiver already holds, for building a plan. */
export async function sentKeys(receiverId: string): Promise<Set<string>> {
  const rows = await (await db()).getAllAsync<{ asset_key: string }>(
    'SELECT asset_key FROM sent WHERE receiver_id = ?',
    receiverId,
  );
  return new Set(rows.map((row) => row.asset_key));
}

/**
 * Where this receiver put each file it already has, by ledger key.
 *
 * Only delete-after-transfer asks for this: an asset skipped because it went
 * on an earlier run is still on the phone, and the user who has just turned
 * the setting on means it too.
 */
export async function sentPaths(receiverId: string): Promise<Map<string, string>> {
  const rows = await (await db()).getAllAsync<{ asset_key: string; stored_path: string | null }>(
    'SELECT asset_key, stored_path FROM sent WHERE receiver_id = ?',
    receiverId,
  );

  const out = new Map<string, string>();
  for (const row of rows) {
    if (row.stored_path) out.set(row.asset_key, row.stored_path);
  }
  return out;
}

export async function recordSent(
  receiverId: string,
  asset: Asset,
  storedPath: string,
): Promise<void> {
  await recordSentKey(receiverId, ledgerKey(asset), asset.size, storedPath);
}

/**
 * The same, for an arrival the app learned about second-hand.
 *
 * A background upload finishes in a system process and is reported to whatever
 * launch of the app happens to hear about it, which has the asset's identifier
 * and size but not the `Asset` itself -- the photo library was never opened in
 * that process.
 */
export async function recordSentKey(
  receiverId: string,
  assetKey: string,
  size: number,
  storedPath: string,
): Promise<void> {
  await (await db()).runAsync(
    `INSERT INTO sent (receiver_id, asset_key, stored_path, size, sent_at)
     VALUES (?, ?, ?, ?, ?)
     ON CONFLICT(receiver_id, asset_key) DO UPDATE SET
       stored_path = excluded.stored_path,
       sent_at     = excluded.sent_at`,
    receiverId,
    assetKey,
    storedPath,
    size,
    Date.now(),
  );
}

/** Forgets a receiver's history, so the next run sends everything again. */
export async function forgetReceiverHistory(receiverId: string): Promise<void> {
  await (await db()).runAsync('DELETE FROM sent WHERE receiver_id = ?', receiverId);
}

export async function sentCount(receiverId: string): Promise<number> {
  const row = await (await db()).getFirstAsync<{ count: number }>(
    'SELECT COUNT(*) AS count FROM sent WHERE receiver_id = ?',
    receiverId,
  );
  return row?.count ?? 0;
}
