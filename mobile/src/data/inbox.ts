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

// What this phone has collected from a receiver.
//
// The point of this record is one specific failure. A file is saved, and then
// the acknowledgement to the receiver does not get through -- the app is
// backgrounded, the network drops, the receiver is asleep. The receiver still
// has the item on offer, so the next check would download it again and the
// user would find two copies of the same video in their camera roll. A row
// here says "already dealt with", and `unackedAt` remembers that the receiver
// has not been told yet, so the acknowledgement is retried instead.

import type { Destination } from '../core/inbox';
import { db } from './db';

export type ReceivedItem = {
  itemId: string;
  filename: string;
  size: number;
  destination: Destination;
  savedAt: number;
};

/** The item ids this phone has already saved from one receiver. */
export async function receivedIds(receiverId: string): Promise<Set<string>> {
  const rows = await (await db()).getAllAsync<{ item_id: string }>(
    'SELECT item_id FROM received WHERE receiver_id = ?',
    receiverId,
  );
  return new Set(rows.map((row) => row.item_id));
}

/**
 * Records a file as saved, and as not yet acknowledged.
 *
 * Written *before* the receiver is told, never after. The order matters: a
 * crash between saving and acknowledging must leave a phone that knows it has
 * the file, not one that collects it again.
 */
export async function recordReceived(
  receiverId: string,
  item: { id: string; filename: string; size: number; sha256: string },
  destination: Destination,
): Promise<void> {
  const now = Date.now();
  await (
    await db()
  ).runAsync(
    `INSERT INTO received (receiver_id, item_id, filename, size, sha256, destination, saved_at, unacked_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT(receiver_id, item_id) DO UPDATE SET
       filename    = excluded.filename,
       destination = excluded.destination,
       saved_at    = excluded.saved_at`,
    receiverId,
    item.id,
    item.filename,
    item.size,
    item.sha256,
    destination,
    now,
    now,
  );
}

/** Marks the receiver as told, so the acknowledgement is not sent again. */
export async function markAcknowledged(receiverId: string, itemId: string): Promise<void> {
  await (
    await db()
  ).runAsync(
    'UPDATE received SET unacked_at = NULL WHERE receiver_id = ? AND item_id = ?',
    receiverId,
    itemId,
  );
}

/** Items this phone has saved but never managed to acknowledge. */
export async function unacknowledged(receiverId: string): Promise<string[]> {
  const rows = await (await db()).getAllAsync<{ item_id: string }>(
    'SELECT item_id FROM received WHERE receiver_id = ? AND unacked_at IS NOT NULL',
    receiverId,
  );
  return rows.map((row) => row.item_id);
}

/** The most recent arrivals, for the screen that shows what turned up. */
export async function recentlyReceived(receiverId: string, limit = 20): Promise<ReceivedItem[]> {
  const rows = await (await db()).getAllAsync<{
    item_id: string;
    filename: string;
    size: number;
    destination: string;
    saved_at: number;
  }>(
    'SELECT item_id, filename, size, destination, saved_at FROM received WHERE receiver_id = ? ORDER BY saved_at DESC LIMIT ?',
    receiverId,
    limit,
  );

  return rows.map((row) => ({
    itemId: row.item_id,
    filename: row.filename,
    size: row.size,
    destination: row.destination === 'photos' ? 'photos' : 'files',
    savedAt: row.saved_at,
  }));
}

/** Forgets a receiver's arrivals, so everything it still offers is collected again. */
export async function forgetReceivedFrom(receiverId: string): Promise<void> {
  await (await db()).runAsync('DELETE FROM received WHERE receiver_id = ?', receiverId);
}
