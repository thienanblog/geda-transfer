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

import {
  backgroundUploadId,
  landed,
  selectForBackground,
  stagedFileName,
  summarize,
  type BackgroundJob,
} from '../background';
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

function job(overrides: Partial<BackgroundJob> = {}): BackgroundJob {
  return {
    uploadId: 'u1',
    receiverId: 'r1',
    assetId: 'a1',
    filename: 'IMG_0001.HEIC',
    state: 'running',
    size: 1000,
    bytesSent: 0,
    storedPath: '',
    deduplicated: false,
    error: '',
    ...overrides,
  };
}

describe('backgroundUploadId', () => {
  it('is safe to put in a filename', () => {
    // A PHAsset identifier holds slashes, and this ends up as a path.
    const id = backgroundUploadId(photo('B84E8479-475C-4727-A4A4-B77AA9980897/L0/001', 42));
    expect(id).not.toContain('/');
    expect(id).toMatch(/^[A-Za-z0-9._-]+$/);
  });

  it('changes when the asset is edited', () => {
    // Same identifier, different bytes: an edit that reused the id must not
    // be mistaken for the photo that is already queued.
    expect(backgroundUploadId(photo('a', 100))).not.toBe(backgroundUploadId(photo('a', 101)));
  });
});

describe('stagedFileName', () => {
  it('keeps the extension and drops the original name', () => {
    // Two photos are routinely both called IMG_0001.HEIC.
    expect(stagedFileName('id-1', 'IMG_0001.HEIC')).toBe('id-1.heic');
    expect(stagedFileName('id-2', 'IMG_0001.HEIC')).toBe('id-2.heic');
  });

  it('copes with a name that has no extension', () => {
    expect(stagedFileName('id-1', 'scan')).toBe('id-1.bin');
  });
});

describe('selectForBackground', () => {
  it('sends the largest first', () => {
    const assets = [photo('small', 10), photo('huge', 1000), photo('middle', 100)];
    const selection = selectForBackground(assets, {
      budget: { maxFiles: 10, maxBytes: 10_000 },
    });

    expect(selection.selected.map((asset) => asset.id)).toEqual(['huge', 'middle', 'small']);
    expect(selection.bytes).toBe(1110);
  });

  it('skips what does not fit rather than stopping', () => {
    // A 4 GB video must not starve two hundred photos that would all have
    // gone in the space it wanted.
    const assets = [photo('video', 900), photo('a', 40), photo('b', 40)];
    const selection = selectForBackground(assets, {
      budget: { maxFiles: 10, maxBytes: 100 },
    });

    expect(selection.selected.map((asset) => asset.id)).toEqual(['a', 'b']);
    expect(selection.deferred.map((asset) => asset.id)).toEqual(['video']);
  });

  it('respects the file count', () => {
    const assets = [photo('a', 1), photo('b', 1), photo('c', 1)];
    const selection = selectForBackground(assets, {
      budget: { maxFiles: 2, maxBytes: 10_000 },
    });

    expect(selection.selected).toHaveLength(2);
    expect(selection.deferred).toHaveLength(1);
  });

  it('leaves half the free space alone', () => {
    // Staging is a real copy of every file. Filling someone's phone with
    // duplicates of their own photos while they are not looking is the worst
    // thing this app could do.
    const assets = [photo('a', 60), photo('b', 60)];
    const selection = selectForBackground(assets, {
      budget: { maxFiles: 10, maxBytes: 10_000 },
      freeBytes: 200,
    });

    expect(selection.selected.map((asset) => asset.id)).toEqual(['a']);
    expect(selection.deferred.map((asset) => asset.id)).toEqual(['b']);
  });

  it('does not queue an asset twice', () => {
    const asset = photo('a', 10);
    const selection = selectForBackground([asset, photo('b', 10)], {
      queued: new Set([backgroundUploadId(asset)]),
    });

    expect(selection.selected.map((entry) => entry.id)).toEqual(['b']);
    expect(selection.skipped.map((entry) => entry.id)).toEqual(['a']);
  });

  it('keeps the members of a pair together', () => {
    const live = [
      photo('live', 300, { pairId: 'p1' }),
      photo('live-video', 20, { pairId: 'p1', kind: 'video' }),
      photo('other', 100),
    ];
    const selection = selectForBackground(live, { budget: { maxFiles: 10, maxBytes: 10_000 } });

    expect(selection.selected.map((asset) => asset.id)).toEqual(['live', 'live-video', 'other']);
  });
});

describe('summarize', () => {
  it('counts a finished file as fully sent', () => {
    // The last progress callback may never arrive: the app can be launched
    // purely to be told the upload is over.
    const summary = summarize([job({ state: 'done', size: 1000, bytesSent: 0 })]);

    expect(summary.bytesSent).toBe(1000);
    expect(summary.filesDone).toBe(1);
    expect(summary.running).toBe(false);
  });

  it('never reports more sent than the file holds', () => {
    const summary = summarize([job({ size: 100, bytesSent: 5000 })]);
    expect(summary.bytesSent).toBe(100);
  });

  it('is running while anything is queued', () => {
    expect(summarize([job({ state: 'pending' }), job({ state: 'done' })]).running).toBe(true);
  });

  it('collapses identical failures', () => {
    // A receiver that has gone away fails every file with the same message.
    const jobs = Array.from({ length: 50 }, (_, index) =>
      job({ uploadId: `u${index}`, filename: 'IMG.HEIC', state: 'failed', error: 'no route' }),
    );
    const summary = summarize(jobs);

    expect(summary.filesFailed).toBe(50);
    expect(summary.errors).toEqual(['IMG.HEIC: no route']);
  });
});

describe('landed', () => {
  it('is only the uploads the receiver confirmed', () => {
    const jobs = [
      job({ uploadId: 'ok', state: 'done' }),
      job({ uploadId: 'bad', state: 'failed' }),
      job({ uploadId: 'busy', state: 'running' }),
    ];

    expect(landed(jobs).map((entry) => entry.uploadId)).toEqual(['ok']);
  });

  it('counts a deduplicated upload as landed', () => {
    // The receiver holds the file, which is all the ledger claims.
    const jobs = [job({ state: 'done', deduplicated: true })];
    expect(landed(jobs)).toHaveLength(1);
  });
});
