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

import type { Asset } from './types';

/**
 * Deciding what to send, and in what order.
 *
 * Order matters more than it looks. With eight uploads in flight, a 4 GB video
 * started last runs alone after everything else has finished, and the link
 * sits mostly idle for the length of that file. Starting the largest first --
 * longest-processing-time-first, the classic scheduling result -- overlaps it
 * with hundreds of small photos instead.
 */

export type Plan = {
  items: Asset[];
  skipped: Asset[];
  totalBytes: number;
};

export type PlanOptions = {
  /** Asset ids this receiver already has, from the local ledger. */
  alreadySent: ReadonlySet<string>;
};

export function buildPlan(assets: Asset[], options: PlanOptions): Plan {
  const items: Asset[] = [];
  const skipped: Asset[] = [];

  for (const asset of assets) {
    if (options.alreadySent.has(ledgerKey(asset))) {
      skipped.push(asset);
    } else {
      items.push(asset);
    }
  }

  const ordered = orderForThroughput(items);
  return {
    items: ordered,
    skipped,
    totalBytes: ordered.reduce((sum, asset) => sum + asset.size, 0),
  };
}

/**
 * Largest first, with the members of a pair kept adjacent.
 *
 * Live Photo and RAW+JPEG members share a basename on the receiver, which it
 * allocates per pair. Sending them near each other is not required -- the
 * receiver copes with any order, because a background session schedules tasks
 * when it pleases (AGENTS.md §3.6) -- but it keeps the pair visible together
 * in the progress list, which is what a person watching expects.
 */
export function orderForThroughput(assets: Asset[]): Asset[] {
  const groups = new Map<string, Asset[]>();
  for (const asset of assets) {
    const key = asset.pairId ?? asset.id;
    const group = groups.get(key);
    if (group) group.push(asset);
    else groups.set(key, [asset]);
  }

  return [...groups.values()]
    .map((group) => ({
      group: [...group].sort((a, b) => b.size - a.size),
      size: group.reduce((sum, asset) => sum + asset.size, 0),
    }))
    .sort((a, b) => b.size - a.size)
    .flatMap((entry) => entry.group);
}

/**
 * The local ledger's key for an asset.
 *
 * The identifier alone is not enough: editing a photo keeps its
 * `localIdentifier` and changes its bytes, and a key that ignored that would
 * quietly never send the edit.
 */
export function ledgerKey(asset: Asset): string {
  return `${asset.id}:${asset.size}`;
}

/**
 * tus metadata for one asset (docs/PROTOCOL.md §5.1).
 *
 * `device_id` and `device_name` are deliberately absent: the receiver sets
 * them from the authenticated token and discards whatever a client sends, so
 * that one device cannot file its uploads under another device's identity.
 */
export function uploadMetadata(asset: Asset): Record<string, string> {
  const metadata: Record<string, string> = {
    filename: asset.filename,
    kind: asset.kind,
  };
  if (asset.capturedAt !== undefined) {
    // The capture date, not the transfer time: the receiver writes it to the
    // stored file's mtime and files it by that date.
    metadata.captured_at = new Date(asset.capturedAt).toISOString();
  }
  if (asset.pairId) {
    metadata.pair_id = asset.pairId;
    metadata.pair_role = asset.kind === 'photo' ? 'primary' : 'secondary';
  }
  if (asset.album) metadata.album = asset.album;
  return metadata;
}
