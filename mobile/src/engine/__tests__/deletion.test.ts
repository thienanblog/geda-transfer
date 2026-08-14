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

// Deleting after transfer, with the device replaced.
//
// The gate for this phase is that deliberate failure injection never deletes
// an unverified file (docs/PLAN.md P9), so most of this file is failures: a
// receiver that refuses, one that says nothing, one that answers about the
// wrong file, one that is unreachable, one that answers with something that
// is not JSON, and a user who taps Cancel.
//
// The assertion in every one of them is the same and it is about a side
// effect, not a return value: `photos.deleted` stayed empty.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { DeletionCandidate } from '../../core/deletion';
import type { Receiver } from '../../core/types';

const DIGEST = 'a'.repeat(64);

const native = {
  /** What `sha256OfFile` reports, by path. */
  digests: new Map<string, string>(),
  requests: [] as { url: string; body: unknown }[],
  /** The canned reply to POST /v1/confirm. */
  reply: { status: 200, body: '{"results":[]}' },
  /** Set to make the request throw, as an unreachable receiver does. */
  unreachable: false,
};

const photos = {
  deleted: [] as string[][],
  /**
   * Set to make the delete throw.
   *
   * That is what both a user tapping Cancel and a library refusal look like:
   * `Asset.delete` goes through `PHPhotoLibrary.performChanges`, which reports
   * a declined confirmation as an error rather than as a false return.
   */
  refuses: false,
};

vi.mock('../../../modules/geda-transfer', () => ({
  default: {
    async request(options: { url: string; body?: string }) {
      if (native.unreachable) throw new Error('the receiver could not be reached');
      native.requests.push({
        url: options.url.replace(/^https?:\/\/[^/]+/, ''),
        body: options.body === undefined ? undefined : JSON.parse(options.body),
      });
      return { status: native.reply.status, headers: {}, body: native.reply.body };
    },
    async race() {
      return 'https://192.168.1.10:47891';
    },
    async sha256OfFile(path: string) {
      const digest = native.digests.get(path);
      if (digest === undefined) throw new Error(`${path} could not be hashed`);
      return digest;
    },
  },
}));

// Shaped like the real module, not like the convenient one. The root
// `deleteAssetsAsync` typechecks and throws at runtime -- it is a deprecation
// shim -- so a mock of that name would have hidden a crash on a device.
vi.mock('expo-media-library', () => ({
  Asset: class {
    id: string;

    constructor(id: string) {
      this.id = id;
    }

    static async delete(assets: { id: string }[]): Promise<void> {
      if (photos.refuses) throw new Error('the library refused it');
      photos.deleted.push(assets.map((asset) => asset.id));
    }
  },
}));

const rows = new Map<string, DeletionCandidate>();
const settings = { deleteAfterTransfer: true };

vi.mock('../../data/deletion', () => ({
  async recordCandidate(_receiverId: string, candidate: DeletionCandidate) {
    rows.set(candidate.key, candidate);
  },
  async pendingCandidates() {
    return [...rows.values()];
  },
  async candidateKeys() {
    return new Set(rows.keys());
  },
  async clearCandidates(_receiverId: string, keys: string[]) {
    for (const key of keys) rows.delete(key);
  },
  async forgetCandidates() {
    rows.clear();
  },
}));

vi.mock('../../data/settings', () => ({
  async loadDeleteAfterTransfer() {
    return settings.deleteAfterTransfer;
  },
}));

vi.mock('../../data/receivers', () => ({
  async rememberAddr() {},
}));

const { noteSent, parseConfirmations, reclaim } = await import('../deletion');

const receiver: Receiver = {
  deviceId: 'receiver-1',
  name: 'Studio Mac',
  spki: 'pin',
  addrs: ['192.168.1.10:47891'],
  token: 'token',
  pairedAt: 0,
};

function candidate(over: Partial<DeletionCandidate> = {}): DeletionCandidate {
  return {
    assetId: 'asset-1',
    key: 'asset-1:1000',
    storedPath: '2026/07/IMG_1.HEIC',
    size: 1000,
    sha256: DIGEST,
    withheld: [],
    filename: 'IMG_1.HEIC',
    ...over,
  };
}

/** The receiver's reply to a batch, as JSON. */
function replies(results: { id: string; confirmed: boolean; reason?: string }[]): string {
  return JSON.stringify({ results });
}

beforeEach(() => {
  rows.clear();
  native.requests = [];
  native.digests.clear();
  native.reply = { status: 200, body: '{"results":[]}' };
  native.unreachable = false;
  photos.deleted = [];
  photos.refuses = false;
  settings.deleteAfterTransfer = true;
});

describe('reclaim', () => {
  it('deletes an asset the receiver vouched for', async () => {
    rows.set('asset-1:1000', candidate());
    native.reply.body = replies([{ id: 'asset-1:1000', confirmed: true }]);

    const outcome = await reclaim(receiver);

    expect(photos.deleted).toEqual([['asset-1']]);
    expect(outcome.deleted).toBe(1);
    expect(rows.size).toBe(0);
  });

  it('asks about the digest and the path the receiver gave it', async () => {
    rows.set('asset-1:1000', candidate());
    native.reply.body = replies([{ id: 'asset-1:1000', confirmed: true }]);

    await reclaim(receiver);

    expect(native.requests).toHaveLength(1);
    expect(native.requests[0].url).toBe('/v1/confirm');
    expect(native.requests[0].body).toEqual({
      items: [{ id: 'asset-1:1000', path: '2026/07/IMG_1.HEIC', size: 1000, sha256: DIGEST }],
    });
  });

  // The setting is the first gate and the last one. Nothing is even asked
  // about when it is off.
  it('does nothing at all when the setting is off', async () => {
    rows.set('asset-1:1000', candidate());
    native.reply.body = replies([{ id: 'asset-1:1000', confirmed: true }]);
    settings.deleteAfterTransfer = false;

    const outcome = await reclaim(receiver);

    expect(outcome.off).toBe(true);
    expect(native.requests).toEqual([]);
    expect(photos.deleted).toEqual([]);
    expect(rows.size).toBe(1);
  });

  describe('deliberate failure injection', () => {
    it.each([
      [
        'the receiver refuses',
        () => {
          native.reply.body = replies([
            { id: 'asset-1:1000', confirmed: false, reason: 'content_mismatch' },
          ]);
        },
      ],
      [
        'the receiver answers about nothing',
        () => {
          native.reply.body = replies([]);
        },
      ],
      [
        'the receiver answers about a different file',
        () => {
          native.reply.body = replies([{ id: 'some-other-file', confirmed: true }]);
        },
      ],
      [
        'the receiver answers with something that is not JSON',
        () => {
          native.reply.body = '<html>504 Gateway Timeout</html>';
        },
      ],
      [
        'the receiver answers with JSON of the wrong shape',
        () => {
          native.reply.body = '{"results":"yes"}';
        },
      ],
      [
        'the receiver says yes with a string rather than a boolean',
        () => {
          native.reply.body = replies([
            { id: 'asset-1:1000', confirmed: 'true' } as unknown as { id: string; confirmed: boolean },
          ]);
        },
      ],
      [
        'the receiver answers with an error status',
        () => {
          native.reply = { status: 500, body: replies([{ id: 'asset-1:1000', confirmed: true }]) };
        },
      ],
      [
        'the receiver no longer recognises this phone',
        () => {
          native.reply = { status: 401, body: '' };
        },
      ],
      [
        'the receiver cannot be reached',
        () => {
          native.unreachable = true;
        },
      ],
    ])('deletes nothing when %s', async (_name, inject) => {
      rows.set('asset-1:1000', candidate());
      inject();

      const outcome = await reclaim(receiver);

      expect(photos.deleted).toEqual([]);
      expect(outcome.deleted).toBe(0);
      // The candidate survives, so the next run asks again rather than
      // forgetting that the file was ever a candidate.
      expect(rows.size).toBe(1);
    });

    // A confirmed still and an unanswered video is the shape that loses the
    // motion half of a Live Photo forever.
    it('deletes neither half of a pair when only one was confirmed', async () => {
      rows.set('asset-1:1000', candidate({ key: 'asset-1:1000' }));
      rows.set('asset-1:pairedVideo', candidate({ key: 'asset-1:pairedVideo', filename: 'IMG_1.MOV' }));
      native.reply.body = replies([{ id: 'asset-1:1000', confirmed: true }]);

      const outcome = await reclaim(receiver);

      expect(photos.deleted).toEqual([]);
      expect(outcome.kept[0].reason).toBe('unanswered');
    });

    it('deletes nothing when part of the asset was never sent', async () => {
      rows.set('asset-1:1000', candidate({ withheld: ['photo'] }));
      native.reply.body = replies([{ id: 'asset-1:1000', confirmed: true }]);

      const outcome = await reclaim(receiver);

      expect(photos.deleted).toEqual([]);
      expect(outcome.kept[0]).toMatchObject({ reason: 'withheld', detail: 'photo' });
    });
  });

  // A declined system dialog and a library refusal arrive the same way, and
  // both must leave the candidate in place rather than throwing out of here.
  it('keeps the candidate when the delete does not happen', async () => {
    rows.set('asset-1:1000', candidate());
    native.reply.body = replies([{ id: 'asset-1:1000', confirmed: true }]);
    photos.refuses = true;

    const outcome = await reclaim(receiver);

    expect(outcome.offered).toBe(1);
    expect(outcome.deleted).toBe(0);
    expect(outcome.errors).toHaveLength(1);
    expect(rows.size).toBe(1);
  });

  // One dialog, not one per photograph: a person deleting four hundred files
  // will not read four hundred prompts, they will learn to tap through them.
  it('asks the system once for every asset', async () => {
    const results: { id: string; confirmed: boolean }[] = [];
    for (let i = 0; i < 25; i += 1) {
      const key = `asset-${i}:1000`;
      rows.set(key, candidate({ assetId: `asset-${i}`, key }));
      results.push({ id: key, confirmed: true });
    }
    native.reply.body = replies(results);

    await reclaim(receiver);

    expect(photos.deleted).toHaveLength(1);
    expect(photos.deleted[0]).toHaveLength(25);
  });

  // Every item in a confirmation request costs the receiver a full read of
  // the file, and these rows are never cleared, so a library of edited photos
  // would re-read every one of them on every transfer.
  it('does not ask about candidates no answer could free', async () => {
    rows.set('asset-1:1000', candidate({ withheld: ['photo'] }));
    rows.set('asset-2:1000', candidate({ assetId: 'asset-2', key: 'asset-2:1000' }));
    native.reply.body = replies([{ id: 'asset-2:1000', confirmed: true }]);

    await reclaim(receiver);

    expect(native.requests).toHaveLength(1);
    expect(native.requests[0].body).toEqual({
      items: [{ id: 'asset-2:1000', path: '2026/07/IMG_1.HEIC', size: 1000, sha256: DIGEST }],
    });
    expect(photos.deleted).toEqual([['asset-2']]);
  });

  it('contacts the receiver at all only when something could be freed', async () => {
    rows.set('asset-1:1000', candidate({ withheld: ['photo'] }));

    const outcome = await reclaim(receiver);

    expect(native.requests).toEqual([]);
    expect(photos.deleted).toEqual([]);
    // Still reported, so the summary says why rather than showing nothing.
    expect(outcome.kept).toHaveLength(1);
    expect(outcome.kept[0].reason).toBe('withheld');
  });

  it('has nothing to do when nothing is waiting', async () => {
    const outcome = await reclaim(receiver);

    expect(outcome).toMatchObject({ off: false, offered: 0, deleted: 0 });
    expect(native.requests).toEqual([]);
  });

  it('only clears the candidates whose assets actually went', async () => {
    rows.set('good:1', candidate({ assetId: 'good', key: 'good:1' }));
    rows.set('bad:1', candidate({ assetId: 'bad', key: 'bad:1' }));
    native.reply.body = replies([
      { id: 'good:1', confirmed: true },
      { id: 'bad:1', confirmed: false, reason: 'missing' },
    ]);

    await reclaim(receiver);

    expect(photos.deleted).toEqual([['good']]);
    expect([...rows.keys()]).toEqual(['bad:1']);
  });
});

describe('noteSent', () => {
  it('records the digest of the file that was sent', async () => {
    native.digests.set('/library/IMG_1.HEIC', DIGEST);

    await noteSent(
      receiver,
      {
        id: 'asset-1',
        filename: 'IMG_1.HEIC',
        filePath: '/library/IMG_1.HEIC',
        size: 1000,
        kind: 'photo',
        withheld: [],
      },
      '2026/07/IMG_1.HEIC',
    );

    expect(rows.get('asset-1:1000')).toMatchObject({
      assetId: 'asset-1',
      sha256: DIGEST,
      storedPath: '2026/07/IMG_1.HEIC',
      withheld: [],
    });
  });

  // No digest, no row; no row, no deletion. A hashing failure must not become
  // a candidate that is confirmed on something else's word.
  it('records nothing when the file cannot be hashed', async () => {
    await noteSent(
      receiver,
      {
        id: 'asset-1',
        filename: 'IMG_1.HEIC',
        filePath: '/library/gone.HEIC',
        size: 1000,
        kind: 'photo',
        withheld: [],
      },
      '2026/07/IMG_1.HEIC',
    );

    expect(rows.size).toBe(0);
  });

  it('records nothing when the receiver never said where the file went', async () => {
    native.digests.set('/library/IMG_1.HEIC', DIGEST);

    await noteSent(
      receiver,
      {
        id: 'asset-1',
        filename: 'IMG_1.HEIC',
        filePath: '/library/IMG_1.HEIC',
        size: 1000,
        kind: 'photo',
        withheld: [],
      },
      '',
    );

    expect(rows.size).toBe(0);
  });

  // The resolver leaves `withheld` undefined when it could not establish what
  // an asset holds, and that has to survive the round trip as undefined.
  it('keeps an unestablished resource list unestablished', async () => {
    native.digests.set('/library/IMG_1.HEIC', DIGEST);

    await noteSent(
      receiver,
      {
        id: 'asset-1',
        filename: 'IMG_1.HEIC',
        filePath: '/library/IMG_1.HEIC',
        size: 1000,
        kind: 'photo',
      },
      '2026/07/IMG_1.HEIC',
    );

    expect(rows.get('asset-1:1000')?.withheld).toBeUndefined();
  });
});

describe('parseConfirmations', () => {
  it.each([
    ['not JSON at all', 'nonsense'],
    ['an empty body', ''],
    ['JSON that is not an object', '"yes"'],
    ['null', 'null'],
    ['no results field', '{}'],
    ['results that are not an array', '{"results":{"id":"a","confirmed":true}}'],
  ])('reads %s as no confirmations', (_name, body) => {
    expect(parseConfirmations(body)).toEqual([]);
  });

  it('drops entries with no usable key', () => {
    const body = JSON.stringify({
      results: [{ confirmed: true }, { id: '', confirmed: true }, { id: 'ok', confirmed: true }],
    });

    expect(parseConfirmations(body)).toEqual([{ key: 'ok', confirmed: true, reason: undefined }]);
  });

  it('treats anything but the boolean true as unconfirmed', () => {
    const body = JSON.stringify({
      results: [
        { id: 'a', confirmed: 'true' },
        { id: 'b', confirmed: 1 },
        { id: 'c' },
        { id: 'd', confirmed: true },
      ],
    });

    expect(parseConfirmations(body).map((c) => c.confirmed)).toEqual([false, false, false, true]);
  });
});
