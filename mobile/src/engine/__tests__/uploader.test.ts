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

// The engine, with the device replaced.
//
// Everything the phone provides -- the photo library, the keychain, the
// URLSession that moves the bytes -- is a boundary the engine talks to through
// a handful of functions, so the scheduling, the resuming, and the bookkeeping
// can all be exercised here rather than only on a device.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { NO_FLAGS } from '../../core/selection';
import type { Asset, Receiver } from '../../core/types';

const native = {
  uploads: [] as { uploadId: string; location?: string; size: number }[],
  listeners: new Map<string, (event: unknown) => void>(),
  /** Uploads that hang until released, keyed by upload id. */
  gates: new Map<string, () => void>(),
  hangFor: new Set<string>(),
  cancelled: [] as string[],
  peakConcurrency: 0,
  inFlight: 0,
  dedup: new Set<string>(),
  /** Rejects a hanging upload, the way URLSession's cancel does. */
  cancelHooks: new Map<string, () => void>(),
};

vi.mock('../../../modules/geda-transfer', () => ({
  default: {
    addListener(name: string, handler: (event: unknown) => void) {
      native.listeners.set(name, handler);
      return { remove: () => native.listeners.delete(name) };
    },
    async race() {
      return 'https://receiver.test:47891';
    },
    async request() {
      return { status: 200, headers: {}, body: '{}' };
    },
    async upload(options: { uploadId: string; location?: string; size: number }) {
      native.uploads.push({ ...options });
      native.inFlight += 1;
      native.peakConcurrency = Math.max(native.peakConcurrency, native.inFlight);

      const location = options.location ?? `https://receiver.test:47891/v1/files/${options.uploadId}`;
      native.listeners.get('onUploadCreated')?.({ uploadId: options.uploadId, location });

      try {
        if (native.hangFor.has(options.uploadId)) {
          await new Promise<void>((resolve, reject) => {
            native.gates.set(options.uploadId, resolve);
            native.cancelHooks.set(options.uploadId, () =>
              reject(new Error('URLSession: cancelled')),
            );
          });
        }

        // A real upload always yields; without a turn of the event loop here
        // the pool would look single-threaded because every "upload" would
        // finish before the next one was started.
        await new Promise((resolve) => setTimeout(resolve, 1));

        native.listeners.get('onUploadProgress')?.({
          uploadId: options.uploadId,
          bytesSent: options.size,
          totalBytes: options.size,
        });

        return {
          location,
          status: 204,
          bytesSent: options.size,
          storedPath: `2026/${options.uploadId}.heic`,
          deduplicated: native.dedup.has(options.uploadId),
          resumedFrom: options.location ? Math.floor(options.size / 2) : 0,
        };
      } finally {
        native.inFlight -= 1;
      }
    },
    async cancel(uploadId: string) {
      native.cancelled.push(uploadId);
      native.cancelHooks.get(uploadId)?.();
    },
    async cancelAll() {
      for (const hook of native.cancelHooks.values()) hook();
    },
  },
}));

const ledger = {
  sent: new Map<string, string>(),
  /** Makes the ledger unreadable, so `run` throws after resolving. */
  failKeys: false,
};

vi.mock('../../data/ledger', () => ({
  async sentKeys() {
    if (ledger.failKeys) throw new Error('the ledger could not be read');
    return new Set(ledger.sent.keys());
  },
  async recordSent(_receiverId: string, asset: Asset, storedPath: string) {
    ledger.sent.set(`${asset.id}:${asset.size}`, storedPath);
  },
  async sentPaths() {
    return new Map(ledger.sent);
  },
  async forgetReceiverHistory() {},
}));

/** What the uploader handed to delete-after-transfer, by stored path. */
const noted: { assetId: string; storedPath: string }[] = [];

// Mocked at the boundary rather than left real: the deletion engine reaches
// expo-media-library, and what this file is testing is only whether the
// uploader offers it anything. Whether the offer becomes a deletion is
// src/engine/__tests__/deletion.test.ts.
vi.mock('../deletion', () => ({
  async noteSent(_receiver: unknown, asset: Asset, storedPath: string) {
    noted.push({ assetId: asset.id, storedPath });
  },
  async alreadyNoted() {
    return new Set<string>();
  },
}));

vi.mock('../../media/library', () => ({
  AssetUnavailableError: class extends Error {},
  release: (asset: Asset) => {
    if (asset.staged) released.push(asset.filePath);
  },
  // One summary resolves to one file here unless the test says otherwise: the
  // several-resources case is `extraResources`, set per test.
  async resolveAsset(summary: { id: string; filename: string; kind: 'photo' | 'video' }) {
    if (summary.id === 'in-icloud') throw new Error('is in iCloud and not on this device');
    const primary: Asset = {
      id: summary.id,
      filename: summary.filename,
      filePath: `/tmp/${summary.filename}`,
      size: sizes[summary.id] ?? 1_000_000,
      kind: summary.kind,
    };
    return [primary, ...(extraResources[summary.id] ?? [])];
  },
}));

/** Extra resources one asset resolves to, keyed by asset id. */
let extraResources: Record<string, Asset[]> = {};
let released: string[] = [];

vi.mock('../session', () => ({
  async connect() {
    return 'https://receiver.test:47891';
  },
  ConnectError: class extends Error {},
}));

const sizes: Record<string, number> = {};

const receiver: Receiver = {
  deviceId: 'receiver-1',
  name: 'NAS',
  spki: 'pin',
  addrs: ['10.0.0.2:47891'],
  token: 'token',
  pairedAt: 0,
};

function summaries(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    id: `asset-${index}`,
    filename: `IMG_${index}.HEIC`,
    kind: 'photo' as const,
    capturedAt: Date.UTC(2026, 6, 4),
    flags: NO_FLAGS,
  }));
}

const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  extraResources = {};
  released = [];
  ledger.failKeys = false;
  native.uploads = [];
  native.listeners.clear();
  native.gates.clear();
  native.hangFor.clear();
  native.cancelled = [];
  native.peakConcurrency = 0;
  native.inFlight = 0;
  native.dedup.clear();
  native.cancelHooks.clear();
  ledger.sent.clear();
  noted.length = 0;
  for (const key of Object.keys(sizes)) delete sizes[key];
});

describe('Transfer', () => {
  // Delete-after-transfer is off unless the user turned it on, so the engine
  // must not even record a candidate by default.
  it('offers nothing for deletion unless asked to', async () => {
    const { Transfer } = await import('../uploader');
    const transfer = new Transfer({ receiver, onChange: () => {} });

    await transfer.run(summaries(3));

    expect(noted).toEqual([]);
  });

  it('records what it sent when delete-after-transfer is on', async () => {
    const { Transfer } = await import('../uploader');
    const transfer = new Transfer({ receiver, deleteAfterTransfer: true, onChange: () => {} });

    await transfer.run(summaries(3));

    expect(noted).toHaveLength(3);
    expect(noted[0].storedPath).toMatch(/^2026\//);
  });

  // An asset that went on an earlier run is still on the phone, and the user
  // who has just turned the setting on means it too.
  it('records assets this receiver already had', async () => {
    ledger.sent.set('asset-0:1000000', '2026/IMG_0.heic');

    const { Transfer } = await import('../uploader');
    const transfer = new Transfer({ receiver, deleteAfterTransfer: true, onChange: () => {} });

    const result = await transfer.run(summaries(2));

    expect(result.alreadyThere).toBe(1);
    expect(noted.map((entry) => entry.assetId).sort()).toEqual(['asset-0', 'asset-1']);
  });

  it('sends everything and records what landed', async () => {
    const { Transfer } = await import('../uploader');
    const transfer = new Transfer({ receiver, onChange: () => {} });

    const result = await transfer.run(summaries(12));

    expect(result.phase).toBe('done');
    expect(result.filesDone).toBe(12);
    expect(result.bytesSent).toBe(12_000_000);
    expect(ledger.sent.size).toBe(12);
    // The measurement the gate is built from has to be a real elapsed time.
    expect(result.transferMs).toBeGreaterThanOrEqual(0);
  });

  // One asset in the library is often several files. A Live Photo that sent
  // only its still would arrive as a photograph that no longer moves.
  it('sends every file an asset resolves to', async () => {
    extraResources['asset-0'] = [
      {
        id: 'asset-0',
        filename: 'IMG_0.MOV',
        filePath: '/cache/geda-resources/asset-0-pairedVideo-IMG_0.MOV',
        size: 4_000_000,
        kind: 'video',
        pairId: 'asset-0',
        pairRole: 'secondary',
        resourceType: 'pairedVideo',
        staged: true,
      },
    ];

    const { Transfer } = await import('../uploader');
    const result = await new Transfer({ receiver, onChange: () => {} }).run(summaries(2));

    expect(result.filesTotal).toBe(3);
    expect(result.filesDone).toBe(3);
    expect(ledger.sent.size).toBe(3);
  });

  // A copy made to reach a Live Photo's video is this transfer's to delete.
  // Left behind, one library import fills the phone with a second copy of
  // everything it just sent.
  it('deletes the copies it made once they have been sent', async () => {
    extraResources['asset-0'] = [
      {
        id: 'asset-0',
        filename: 'IMG_0.MOV',
        filePath: '/cache/geda-resources/asset-0-pairedVideo-IMG_0.MOV',
        size: 4_000_000,
        kind: 'video',
        pairRole: 'secondary',
        resourceType: 'pairedVideo',
        staged: true,
      },
    ];

    const { Transfer } = await import('../uploader');
    await new Transfer({ receiver, onChange: () => {} }).run(summaries(1));

    expect(released).toEqual(['/cache/geda-resources/asset-0-pairedVideo-IMG_0.MOV']);
  });

  // An asset the receiver already has was still copied out of the library to
  // be looked at, and it is skipped rather than sent -- so nothing else would
  // ever delete it.
  it('deletes copies for resources the receiver already has', async () => {
    ledger.sent.set('asset-0:pairedVideo:4000000', 'already/there.MOV');
    extraResources['asset-0'] = [
      {
        id: 'asset-0',
        filename: 'IMG_0.MOV',
        filePath: '/cache/geda-resources/asset-0-pairedVideo-IMG_0.MOV',
        size: 4_000_000,
        kind: 'video',
        pairRole: 'secondary',
        resourceType: 'pairedVideo',
        staged: true,
      },
    ];

    const { Transfer } = await import('../uploader');
    const result = await new Transfer({ receiver, onChange: () => {} }).run(summaries(1));

    expect(result.alreadyThere).toBe(1);
    expect(released).toEqual(['/cache/geda-resources/asset-0-pairedVideo-IMG_0.MOV']);
  });

  // A cancelled run that kept its copies would fill the phone with a second
  // copy of a library it never sent.
  it('deletes its copies even when the transfer fails outright', async () => {
    extraResources['asset-0'] = [
      {
        id: 'asset-0',
        filename: 'IMG_0.MOV',
        filePath: '/cache/geda-resources/asset-0-pairedVideo-IMG_0.MOV',
        size: 4_000_000,
        kind: 'video',
        pairRole: 'secondary',
        resourceType: 'pairedVideo',
        staged: true,
      },
    ];
    ledger.failKeys = true;

    const { Transfer } = await import('../uploader');
    await expect(new Transfer({ receiver, onChange: () => {} }).run(summaries(1))).rejects.toThrow();

    expect(released).toEqual(['/cache/geda-resources/asset-0-pairedVideo-IMG_0.MOV']);
  });

  it('keeps the agreed number of streams in flight', async () => {
    // Six to eight over one HTTP/2 connection is what saturates the link;
    // more only makes the receiver's disk seek (AGENTS.md §3.2).
    const { Transfer } = await import('../uploader');
    const transfer = new Transfer({ receiver, concurrency: 4, onChange: () => {} });

    await transfer.run(summaries(20));

    expect(native.peakConcurrency).toBeLessThanOrEqual(4);
    expect(native.peakConcurrency).toBeGreaterThan(1);
  });

  it('skips what the receiver already had, without counting it as an error', async () => {
    const { Transfer } = await import('../uploader');
    native.dedup.add('asset-0-1000000');

    const result = await transfer(Transfer).run(summaries(3));

    const skipped = result.items.filter((item) => item.state === 'skipped');
    expect(skipped).toHaveLength(1);
    expect(result.errors).toHaveLength(0);
  });

  it('resumes a paused file instead of sending it again', async () => {
    const { Transfer } = await import('../uploader');
    const engine = new Transfer({ receiver, concurrency: 1, onChange: () => {} });
    native.hangFor.add('asset-0-1000000');

    const finished = engine.run(summaries(1));
    await tick();
    await tick();

    engine.pause();
    await tick();

    // Cancelling the in-flight request is what makes pause immediate; the
    // upload URL is remembered so the next attempt asks for the offset.
    expect(native.cancelled).toContain('asset-0-1000000');

    native.hangFor.clear();
    engine.resume();
    const result = await finished;

    expect(result.phase).toBe('done');
    expect(native.uploads).toHaveLength(2);
    expect(native.uploads[0]?.location).toBeUndefined();
    expect(native.uploads[1]?.location).toBe(
      'https://receiver.test:47891/v1/files/asset-0-1000000',
    );
  });

  it('reports an unreadable asset and sends the rest', async () => {
    // One photo stuck in iCloud must not end a transfer of four hundred.
    const { Transfer } = await import('../uploader');
    const engine = new Transfer({ receiver, onChange: () => {} });

    const result = await engine.run([
      { id: 'in-icloud', filename: 'IMG_1.HEIC', kind: 'photo' as const, flags: NO_FLAGS },
      ...summaries(2),
    ]);

    expect(result.filesTotal).toBe(2);
    expect(result.errors.join(' ')).toContain('iCloud');
    expect(result.phase).toBe('done');
  });

  it('measures the library and the network separately', async () => {
    // A transfer rate that looks excellent next to a much lower wall-clock
    // rate is the signature of the export being the bottleneck, which is what
    // AGENTS.md §5 says to go after first.
    const { Transfer } = await import('../uploader');
    const result = await transfer(Transfer).run(summaries(4));

    expect(result.prepareMs).toBeGreaterThanOrEqual(0);
    expect(result.transferMs).toBeGreaterThanOrEqual(0);
  });
});

function transfer(Transfer: typeof import('../uploader').Transfer) {
  return new Transfer({ receiver, onChange: () => {} });
}
