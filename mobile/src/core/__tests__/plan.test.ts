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

import { describe, expect, it } from 'vitest';

import { buildPlan, ledgerKey, orderForThroughput, uploadMetadata } from '../plan';
import type { Asset } from '../types';

function photo(id: string, size: number, extra: Partial<Asset> = {}): Asset {
  return {
    id,
    filename: `${id}.HEIC`,
    filePath: `/tmp/${id}.HEIC`,
    size,
    kind: 'photo',
    ...extra,
  };
}

describe('buildPlan', () => {
  it('skips what this receiver already has', () => {
    const assets = [photo('a', 100), photo('b', 200)];
    const plan = buildPlan(assets, { alreadySent: new Set([ledgerKey(assets[0]!)]) });

    expect(plan.items.map((asset) => asset.id)).toEqual(['b']);
    expect(plan.skipped.map((asset) => asset.id)).toEqual(['a']);
    expect(plan.totalBytes).toBe(200);
  });

  it('sends an edited photo again', () => {
    // Editing keeps the asset's identifier and changes its bytes. A ledger
    // keyed on the identifier alone would silently never send the edit.
    const original = photo('a', 100);
    const edited = photo('a', 140);

    const plan = buildPlan([edited], { alreadySent: new Set([ledgerKey(original)]) });
    expect(plan.items.map((asset) => asset.id)).toEqual(['a']);
  });
});

describe('orderForThroughput', () => {
  it('starts with the largest, so the long file overlaps the small ones', () => {
    // A 4 GB video started last runs alone after everything else finished,
    // and the link sits idle for the length of it.
    const ordered = orderForThroughput([
      photo('small', 1_000),
      photo('video', 4_000_000_000, { kind: 'video' }),
      photo('medium', 5_000_000),
    ]);

    expect(ordered.map((asset) => asset.id)).toEqual(['video', 'medium', 'small']);
  });

  it('keeps the members of a pair together', () => {
    // Live Photo members share a basename on the receiver, allocated once per
    // pair. Keeping them adjacent is what a person watching the list expects.
    const ordered = orderForThroughput([
      photo('still', 3_000_000, { pairId: 'live-1' }),
      photo('other', 4_000_000),
      photo('movie', 2_000_000, { pairId: 'live-1', kind: 'video' }),
    ]);

    const positions = ordered.map((asset) => asset.id);
    expect(Math.abs(positions.indexOf('still') - positions.indexOf('movie'))).toBe(1);
    // The pair is 5 MB together, so it outweighs the single 4 MB file.
    expect(positions[0]).toBe('still');
  });
});

describe('uploadMetadata', () => {
  it('sends the capture date, not the transfer time', () => {
    const captured = Date.UTC(2026, 6, 4, 15, 9, 3);
    const metadata = uploadMetadata(photo('a', 10, { capturedAt: captured }));

    expect(metadata.captured_at).toBe('2026-07-04T15:09:03.000Z');
    expect(metadata.filename).toBe('a.HEIC');
    expect(metadata.kind).toBe('photo');
  });

  it('never claims a device identity', () => {
    // The receiver sets device_id and device_name from the authenticated
    // token and discards what the client sends; sending them would only
    // suggest they mean something (docs/PROTOCOL.md §5.1).
    const metadata = uploadMetadata(photo('a', 10));
    expect(metadata.device_id).toBeUndefined();
    expect(metadata.device_name).toBeUndefined();
  });

  it('marks both members of a pair', () => {
    // The role comes from the selection, not from the kind: a RAW+JPEG pair
    // is two photos, and an edited photo sent beside its original is two
    // photos as well, so "the video is the secondary one" is not a rule.
    const still = uploadMetadata(photo('a', 10, { pairId: 'live-1', pairRole: 'primary' }));
    const movie = uploadMetadata(
      photo('b', 10, { pairId: 'live-1', kind: 'video', pairRole: 'secondary' }),
    );

    expect(still.pair_id).toBe('live-1');
    expect(still.pair_role).toBe('primary');
    expect(movie.pair_id).toBe('live-1');
    expect(movie.pair_role).toBe('secondary');
  });

  it('defaults a pair member with no role to the primary', () => {
    // Two primaries is a pair that shares a basename and has no order, which
    // is harmless. A missing role that became "secondary" would be a pair
    // with no primary at all.
    const metadata = uploadMetadata(photo('a', 10, { pairId: 'live-1' }));
    expect(metadata.pair_role).toBe('primary');
  });
});
