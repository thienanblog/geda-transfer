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

// The hand-over to the system, with the device replaced.
//
// What is worth testing here is not the transfer -- that belongs to
// `nsurlsessiond` and can only be seen on a phone -- but everything the app
// does in the one moment it is still in control: which files it stages, what
// it does with a copy the system would not accept, and whether an upload that
// finished while the app was dead reaches the ledger.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { BackgroundJob } from '../../core/background';
import type { Receiver } from '../../core/types';

const native = {
  jobs: [] as BackgroundJob[],
  started: [] as { uploadId: string; stagedPath: string; size: number }[],
  /** Upload ids `startBackground` refuses, as an unreachable receiver would. */
  refuse: new Set<string>(),
  cleared: 0,
  kickoffs: 0,
};

const disk = {
  files: new Set<string>(),
  copies: [] as { from: string; to: string }[],
  deleted: [] as string[],
  free: undefined as number | undefined,
};

vi.mock('../../../modules/geda-transfer', () => ({
  default: {
    addListener() {
      return { remove() {} };
    },
    backgroundStagingDirectory() {
      return '/container/staging';
    },
    backgroundJobs() {
      return native.jobs;
    },
    async startBackground(requests: { uploadId: string; stagedPath: string; size: number }[]) {
      const accepted = requests.filter((request) => !native.refuse.has(request.uploadId));
      native.started.push(...accepted);
      return accepted.map((request) => request.uploadId);
    },
    async reconcileBackground() {
      return native.jobs;
    },
    async retryBackground() {
      return native.jobs;
    },
    clearDeliveredBackground() {
      native.cleared += 1;
      // Failures stay: they are what the next wake-up retries.
      native.jobs = native.jobs.filter((job) => job.state !== 'done');
    },
    cancelBackground() {
      native.jobs = [];
    },
    scheduleBackgroundKickoff() {
      native.kickoffs += 1;
    },
  },
}));

vi.mock('expo-file-system', () => {
  class FakeFile {
    uri: string;

    constructor(...parts: (string | { uri: string })[]) {
      const joined = parts
        .map((part) => (typeof part === 'string' ? part : part.uri))
        .join('/')
        .replace(/^file:\/\//, '')
        .replace(/\/+/g, '/');
      this.uri = `file://${joined}`;
    }

    get path(): string {
      return this.uri.replace(/^file:\/\//, '');
    }

    get exists(): boolean {
      return disk.files.has(this.path);
    }

    async copy(destination: FakeFile): Promise<void> {
      disk.copies.push({ from: this.path, to: destination.path });
      disk.files.add(destination.path);
    }

    delete(): void {
      disk.deleted.push(this.path);
      disk.files.delete(this.path);
    }
  }

  return {
    File: FakeFile,
    Directory: FakeFile,
    Paths: {
      get availableDiskSpace() {
        return disk.free;
      },
    },
  };
});

const ledger = {
  sent: new Set<string>(),
  recorded: [] as { receiverId: string; key: string; storedPath: string }[],
};

vi.mock('../../data/ledger', () => ({
  async sentKeys() {
    return ledger.sent;
  },
  async recordSentKey(receiverId: string, key: string, _size: number, storedPath: string) {
    ledger.recorded.push({ receiverId, key, storedPath });
  },
}));

vi.mock('../../media/library', () => ({
  async resolveAsset(summary: { id: string; filename: string; kind: 'photo' | 'video' }) {
    if (summary.id === 'in-icloud') throw new Error('is in iCloud and not on this device');
    return {
      id: summary.id,
      filename: summary.filename,
      filePath: `/library/${summary.filename}`,
      size: sizes[summary.id] ?? 1000,
      kind: summary.kind,
    };
  },
}));

vi.mock('../session', () => ({
  async connect() {
    return 'https://receiver.test:47891';
  },
  ConnectError: class extends Error {},
}));

let sizes: Record<string, number> = {};

const {
  cancelBackgroundTransfers,
  recordArrivals,
  resumeBackgroundTransfers,
  startBackgroundTransfer,
} = await import('../background');

const receiver: Receiver = {
  deviceId: 'desk-1',
  name: 'Studio NAS',
  spki: 'pin',
  addrs: ['192.168.1.10:47891'],
  token: 'token',
  pairedAt: 0,
};

function summaries(...ids: string[]) {
  return ids.map((id) => ({ id, filename: `${id}.HEIC`, kind: 'photo' as const }));
}

function job(overrides: Partial<BackgroundJob> = {}): BackgroundJob {
  return {
    uploadId: 'u1',
    receiverId: 'desk-1',
    assetId: 'a1',
    filename: 'a1.HEIC',
    state: 'done',
    size: 1000,
    bytesSent: 1000,
    storedPath: '2026/a1.HEIC',
    deduplicated: false,
    error: '',
    ...overrides,
  };
}

beforeEach(() => {
  native.jobs = [];
  native.started = [];
  native.refuse = new Set();
  native.cleared = 0;
  native.kickoffs = 0;
  disk.files = new Set();
  disk.copies = [];
  disk.deleted = [];
  disk.free = undefined;
  ledger.sent = new Set();
  ledger.recorded = [];
  sizes = {};
});

describe('startBackgroundTransfer', () => {
  it('stages a copy of each file inside the app container', async () => {
    // The library's originals are not readable by the system process that
    // does the sending, so nothing can be handed over in place.
    const result = await startBackgroundTransfer({ receiver, summaries: summaries('a', 'b') });

    expect(result.queued).toBe(2);
    expect(disk.copies.map((copy) => copy.from)).toEqual(['/library/a.HEIC', '/library/b.HEIC']);
    for (const started of native.started) {
      expect(started.stagedPath.startsWith('/container/staging/')).toBe(true);
    }
  });

  it('asks for a wake-up on power and Wi-Fi', async () => {
    await startBackgroundTransfer({ receiver, summaries: summaries('a') });
    expect(native.kickoffs).toBe(1);
  });

  it('does not send what the receiver already has', async () => {
    ledger.sent = new Set(['a:1000']);
    const result = await startBackgroundTransfer({ receiver, summaries: summaries('a', 'b') });

    expect(result.alreadyThere).toBe(1);
    expect(native.started.map((started) => started.size)).toEqual([1000]);
    expect(disk.copies).toHaveLength(1);
  });

  it('does not queue a file that is already in flight', async () => {
    native.jobs = [job({ uploadId: 'a-1000', assetId: 'a', state: 'running' })];
    const result = await startBackgroundTransfer({ receiver, summaries: summaries('a', 'b') });

    expect(result.queued).toBe(1);
    expect(disk.copies.map((copy) => copy.from)).toEqual(['/library/b.HEIC']);
  });

  it('deletes a staged copy the system would not take', async () => {
    // Otherwise a receiver that was unreachable for one file leaves a
    // duplicate of it on the phone with nobody left to clean it up.
    sizes = { a: 10, b: 20 };
    native.refuse = new Set(['a-10']);

    const result = await startBackgroundTransfer({ receiver, summaries: summaries('a', 'b') });

    expect(result.queued).toBe(1);
    expect(disk.deleted).toEqual(['/container/staging/a-10.heic']);
  });

  it('reports the assets it could not read and sends the rest', async () => {
    const result = await startBackgroundTransfer({
      receiver,
      summaries: summaries('a', 'in-icloud'),
    });

    expect(result.queued).toBe(1);
    expect(result.errors).toHaveLength(1);
    expect(result.errors[0]).toContain('iCloud');
  });

  it('stops resolving once the batch is full', async () => {
    // Getting a file out of a PHAsset is the slowest step in a transfer.
    // Resolving ten thousand of them to stage two hundred is minutes of work
    // thrown away.
    const result = await startBackgroundTransfer({
      receiver,
      summaries: summaries('a', 'b', 'c', 'd', 'e'),
      budget: { maxFiles: 2, maxBytes: 1_000_000 },
    });

    expect(result.queued).toBe(2);
    expect(disk.copies).toHaveLength(2);
    // The three never looked at are deferred, not lost.
    expect(result.deferred).toBe(3);
  });

  it('clears the slice an interrupted attempt left behind', async () => {
    // The replacement job records no slice, so nothing else would delete it.
    disk.files.add('/container/staging/a-1000.slice');
    await startBackgroundTransfer({ receiver, summaries: summaries('a') });

    expect(disk.deleted).toContain('/container/staging/a-1000.slice');
  });

  it('leaves what does not fit for the next kickoff', async () => {
    sizes = { a: 100, b: 100 };
    disk.free = 300;

    const result = await startBackgroundTransfer({ receiver, summaries: summaries('a', 'b') });

    expect(result.queued).toBe(1);
    expect(result.deferred).toBe(1);
  });
});

describe('resumeBackgroundTransfers', () => {
  it('writes what landed while the app was dead into the ledger', async () => {
    native.jobs = [
      job({ uploadId: 'u1', assetId: 'a1', size: 1000, storedPath: '2026/a1.HEIC' }),
      job({ uploadId: 'u2', assetId: 'a2', state: 'failed', error: 'no route' }),
    ];

    const summary = await resumeBackgroundTransfers();

    expect(ledger.recorded).toEqual([
      { receiverId: 'desk-1', key: 'a1:1000', storedPath: '2026/a1.HEIC' },
    ]);
    expect(native.cleared).toBe(1);
    // The failure stays: it is what the next wake-up retries, and what the
    // app shows someone wondering where their photo went.
    expect(summary.filesTotal).toBe(1);
    expect(summary.filesFailed).toBe(1);
  });

  it('does not touch the ledger when nothing finished', async () => {
    native.jobs = [job({ state: 'running' })];
    const summary = await resumeBackgroundTransfers();

    expect(ledger.recorded).toEqual([]);
    expect(native.cleared).toBe(0);
    expect(summary.running).toBe(true);
  });

  it('records a deduplicated upload', async () => {
    // The receiver holds the file, which is all the ledger claims.
    native.jobs = [job({ deduplicated: true, storedPath: '2026/original.HEIC' })];
    await resumeBackgroundTransfers();

    expect(ledger.recorded).toHaveLength(1);
  });
});

describe('recordArrivals', () => {
  it('records every delivered job, not just the one that raised the event', async () => {
    // Two uploads finishing at once must not race: clearing on behalf of the
    // first must never discard the second before it is written down.
    native.jobs = [
      job({ uploadId: 'u1', assetId: 'a1' }),
      job({ uploadId: 'u2', assetId: 'a2' }),
    ];

    await recordArrivals();

    expect(ledger.recorded.map((entry) => entry.key)).toEqual(['a1:1000', 'a2:1000']);
    expect(native.jobs).toEqual([]);
  });

  it('leaves the ledger alone when nothing arrived', async () => {
    native.jobs = [job({ state: 'running' })];
    await recordArrivals();

    expect(ledger.recorded).toEqual([]);
    expect(native.cleared).toBe(0);
  });
});

describe('cancelBackgroundTransfers', () => {
  it('drops everything', () => {
    native.jobs = [job({ state: 'running' })];
    cancelBackgroundTransfers();

    expect(native.jobs).toEqual([]);
  });
});
