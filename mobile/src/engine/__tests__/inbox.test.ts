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

// Collecting from a computer, with the device replaced.
//
// The download itself belongs to `nsurlsessiond` and can only be watched on a
// phone. What is testable, and what P7 turns on, is everything the app does
// around it: that nothing is saved before its digest matches, that a photo and
// a ZIP go to different places, that the receiver is not told until the file
// is safely down, and that a lost acknowledgement does not produce two copies
// of the same video.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { DownloadJob } from '../../core/inbox';
import type { Receiver } from '../../core/types';

const native = {
  jobs: [] as DownloadJob[],
  started: [] as { itemId: string; path: string; size: number }[],
  finished: [] as string[],
  failed: [] as { itemId: string; reason: string }[],
  /** What `sha256OfFile` reports, by staged path. */
  digests: new Map<string, string>(),
  requests: [] as { url: string; method: string }[],
  /** Canned replies, by `METHOD path`. */
  replies: new Map<string, { status: number; body: string }>(),
  kickoffs: 0,
};

const disk = {
  received: [] as string[],
  moves: [] as { from: string; to: string }[],
};

const photos = {
  created: [] as string[],
  writable: true,
};

const listeners = new Map<string, (payload: unknown) => void>();

vi.mock('../../../modules/geda-transfer', () => ({
  default: {
    addListener(name: string, handler: (payload: unknown) => void) {
      listeners.set(name, handler);
      return { remove: () => listeners.delete(name) };
    },
    async request(options: { url: string; method?: string }) {
      const method = options.method ?? 'GET';
      const path = options.url.replace(/^https?:\/\/[^/]+/, '');
      native.requests.push({ url: path, method });
      const canned = native.replies.get(`${method} ${path}`);
      return {
        // What the real receiver answers: 204 to an acknowledgement, a
        // listing to everything else.
        status: canned?.status ?? (method === 'DELETE' ? 204 : 200),
        headers: {},
        body: canned?.body ?? '{"items":[]}',
      };
    },
    async race() {
      return 'https://192.168.1.10:47891';
    },
    receivedDirectory() {
      return '/container/Documents/Received';
    },
    downloadJobs() {
      return native.jobs;
    },
    async startDownloads(requests: { itemId: string; path: string; size: number }[]) {
      native.started.push(...requests);
      return requests.map((request) => request.itemId);
    },
    async reconcileDownloads() {
      return native.jobs;
    },
    async retryDownloads() {
      return native.jobs;
    },
    finishDownload(itemId: string) {
      native.finished.push(itemId);
      native.jobs = native.jobs.filter((job) => job.itemId !== itemId);
    },
    failDownload(itemId: string, reason: string) {
      native.failed.push({ itemId, reason });
      native.jobs = native.jobs.filter((job) => job.itemId !== itemId);
    },
    cancelDownloads() {
      native.jobs = [];
    },
    scheduleBackgroundKickoff() {
      native.kickoffs += 1;
    },
    async sha256OfFile(path: string) {
      return native.digests.get(path) ?? 'unknown';
    },
  },
}));

vi.mock('expo-file-system', () => {
  class FakeFile {
    uri: string;
    name: string;

    constructor(...parts: (string | { uri: string })[]) {
      const joined = parts
        .map((part) => (typeof part === 'string' ? part : part.uri))
        .join('/')
        .replace(/^file:\/\//, '')
        .replace(/\/+/g, '/');
      this.uri = `file://${joined}`;
      this.name = joined.slice(joined.lastIndexOf('/') + 1);
    }

    async move(destination: FakeFile): Promise<void> {
      disk.moves.push({ from: this.uri, to: destination.uri });
      disk.received.push(destination.name);
    }
  }

  class FakeDirectory {
    uri: string;
    name = 'Received';

    constructor(path: string) {
      this.uri = `file://${path}`;
    }

    list(): { name: string }[] {
      return disk.received.map((name) => ({ name }));
    }
  }

  return { File: FakeFile, Directory: FakeDirectory, Paths: {} };
});

vi.mock('expo-media-library', () => ({
  Asset: {
    async create(path: string) {
      if (!photos.writable) throw new Error('the library refused it');
      photos.created.push(path);
      return { id: 'asset-1' };
    },
  },
  async getPermissionsAsync() {
    return { granted: photos.writable, canAskAgain: false };
  },
  async requestPermissionsAsync() {
    return { granted: photos.writable, canAskAgain: false };
  },
}));

const ledger = {
  received: new Map<string, { destination: string }>(),
  unacked: new Set<string>(),
};

vi.mock('../../data/inbox', () => ({
  async receivedIds() {
    return new Set([...ledger.received.keys()]);
  },
  async recordReceived(
    _receiverId: string,
    item: { id: string },
    destination: string,
  ): Promise<void> {
    ledger.received.set(item.id, { destination });
    ledger.unacked.add(item.id);
  },
  async markAcknowledged(_receiverId: string, itemId: string): Promise<void> {
    ledger.unacked.delete(itemId);
  },
  async unacknowledged(): Promise<string[]> {
    return [...ledger.unacked];
  },
}));

const settings = { saveMediaToFiles: false };

vi.mock('../../data/settings', () => ({
  async loadInboxSettings() {
    return settings;
  },
}));

vi.mock('../../data/receivers', () => ({
  async rememberAddr() {},
}));

const { checkInbox, resumeInbox, watchInbox } = await import('../inbox');

const digest = 'a'.repeat(64);

const receiver: Receiver = {
  deviceId: 'mac-1',
  name: 'Studio Mac',
  spki: 'pin',
  addrs: ['192.168.1.10:47891'],
  token: 'token',
  pairedAt: 0,
};

function job(over: Partial<DownloadJob> = {}): DownloadJob {
  return {
    itemId: 'i1',
    receiverId: 'mac-1',
    receiverName: 'Studio Mac',
    filename: 'archive.zip',
    kind: 'file',
    capturedAt: '',
    sha256: digest,
    size: 2_000_000_000,
    bytesReceived: 2_000_000_000,
    state: 'ready',
    stagedPath: '/container/incoming/i1.zip',
    error: '',
    ...over,
  };
}

function listing(items: Record<string, unknown>[]): string {
  return JSON.stringify({ items });
}

beforeEach(() => {
  native.jobs = [];
  native.started = [];
  native.finished = [];
  native.failed = [];
  native.requests = [];
  native.digests = new Map();
  native.replies = new Map();
  native.kickoffs = 0;
  disk.received = [];
  disk.moves = [];
  photos.created = [];
  photos.writable = true;
  ledger.received = new Map();
  ledger.unacked = new Set();
  settings.saveMediaToFiles = false;
});

describe('asking a computer what it has', () => {
  it('hands what is offered to the system and asks for a wake-up', async () => {
    native.replies.set(
      'GET /v1/outbox',
      {
        status: 200,
        body: listing([
          { id: 'i1', filename: 'archive.zip', size: 2_000_000_000, sha256: digest, kind: 'file' },
          { id: 'i2', filename: 'clip.mov', size: 5_000, sha256: digest, kind: 'video' },
        ]),
      },
    );

    const result = await checkInbox(receiver);

    expect(result.started).toBe(2);
    // Largest first, so the big one is not left running on its own at the end.
    expect(native.started.map((r) => r.itemId)).toEqual(['i1', 'i2']);
    expect(native.started[0]?.path).toBe('/v1/outbox/i1');
    expect(native.kickoffs).toBe(1);
  });

  it('does not collect what this phone already has', async () => {
    ledger.received.set('i1', { destination: 'files' });
    native.replies.set('GET /v1/outbox', {
      status: 200,
      body: listing([{ id: 'i1', filename: 'archive.zip', size: 10, sha256: digest, kind: 'file' }]),
    });

    const result = await checkInbox(receiver);

    expect(result.started).toBe(0);
    expect(result.alreadyHere).toBe(1);
    expect(native.started).toEqual([]);
  });

  it('says to pair again when the computer no longer recognises this phone', async () => {
    native.replies.set('GET /v1/outbox', { status: 401, body: '{}' });

    await expect(checkInbox(receiver)).rejects.toThrow(/scanning its QR code/);
  });
});

describe('putting an arrival away', () => {
  it('checks the digest before anything else touches the bytes', async () => {
    native.jobs = [job({ kind: 'video', filename: 'clip.mov' })];
    native.digests.set('/container/incoming/i1.zip', 'b'.repeat(64));

    await resumeInbox([receiver]);

    // Nothing saved, nothing recorded, and the bytes discarded with it.
    expect(photos.created).toEqual([]);
    expect(disk.moves).toEqual([]);
    expect(ledger.received.size).toBe(0);
    expect(native.failed[0]?.reason).toMatch(/damaged/);
  });

  it('puts a video in the photo library and a zip in Files', async () => {
    native.jobs = [
      job({ itemId: 'i1', kind: 'video', filename: 'clip.mov', stagedPath: '/incoming/i1.mov' }),
      job({ itemId: 'i2', kind: 'file', filename: 'archive.zip', stagedPath: '/incoming/i2.zip' }),
    ];
    native.digests.set('/incoming/i1.mov', digest);
    native.digests.set('/incoming/i2.zip', digest);

    await resumeInbox([receiver]);

    expect(photos.created).toEqual(['/incoming/i1.mov']);
    expect(disk.moves.map((m) => m.to)).toEqual([
      'file:///container/Documents/Received/archive.zip',
    ]);
    expect(ledger.received.get('i1')?.destination).toBe('photos');
    expect(ledger.received.get('i2')?.destination).toBe('files');
    expect(native.finished).toEqual(['i1', 'i2']);
  });

  it('honours the advanced setting for media, and never for other files', async () => {
    settings.saveMediaToFiles = true;
    native.jobs = [job({ kind: 'photo', filename: 'IMG_4021.HEIC', stagedPath: '/incoming/i1' })];
    native.digests.set('/incoming/i1', digest);

    await resumeInbox([receiver]);

    expect(photos.created).toEqual([]);
    expect(disk.moves[0]?.to).toBe('file:///container/Documents/Received/IMG_4021.HEIC');
  });

  // The name came off a network. It is not a path until it has been made one.
  it('refuses to let a filename become a path', async () => {
    native.jobs = [
      job({ filename: '../../Library/Preferences/evil.plist', stagedPath: '/incoming/i1' }),
    ];
    native.digests.set('/incoming/i1', digest);

    await resumeInbox([receiver]);

    expect(disk.moves[0]?.to).toBe('file:///container/Documents/Received/evil.plist');
  });

  it('numbers a second file rather than overwriting the first', async () => {
    disk.received.push('archive.zip');
    native.jobs = [job({ stagedPath: '/incoming/i1' })];
    native.digests.set('/incoming/i1', digest);

    await resumeInbox([receiver]);

    expect(disk.moves[0]?.to).toBe('file:///container/Documents/Received/archive_1.zip');
  });

  // Throwing the bytes away here would mean downloading gigabytes again only
  // to fail at the same permission prompt.
  it('keeps the download when the photo library refuses it, and says why', async () => {
    photos.writable = false;
    native.jobs = [job({ kind: 'photo', stagedPath: '/incoming/i1' })];
    native.digests.set('/incoming/i1', digest);

    const summary = await resumeInbox([receiver]);

    expect(ledger.received.size).toBe(0);
    expect(native.failed).toEqual([]);
    expect(native.finished).toEqual([]);
    expect(native.jobs).toHaveLength(1);
    expect(summary.errors.join(' ')).toMatch(/photo library|Save to Files/i);

    // And it goes in as soon as the permission is there, with no second
    // download.
    photos.writable = true;
    await resumeInbox([receiver]);
    expect(photos.created).toEqual(['/incoming/i1']);
  });

  // Two downloads finishing a second apart is the ordinary case, not the
  // exotic one, and each finish drives its own save.
  it('does not save the same download twice when two saves overlap', async () => {
    native.jobs = [job({ itemId: 'i1', kind: 'video', stagedPath: '/incoming/i1' })];
    native.digests.set('/incoming/i1', digest);

    await Promise.all([resumeInbox([receiver]), resumeInbox([receiver])]);

    expect(photos.created).toEqual(['/incoming/i1']);
    expect(native.finished).toEqual(['i1']);
  });
});

describe('telling the computer it arrived', () => {
  it('acknowledges only after the file is saved and written down', async () => {
    native.jobs = [job({ stagedPath: '/incoming/i1' })];
    native.digests.set('/incoming/i1', digest);

    // Saving happens with no network at all: a file already on the phone must
    // reach its destination even if the computer has since been closed.
    await resumeInbox([receiver]);
    expect(ledger.received.has('i1')).toBe(true);
    expect(native.requests).toEqual([]);

    await checkInbox(receiver);
    expect(native.requests).toContainEqual({ url: '/v1/outbox/i1', method: 'DELETE' });
    expect(ledger.unacked.has('i1')).toBe(false);
  });

  // The failure this whole ledger exists for: without it the computer still
  // has the item on offer and the user gets the same video twice.
  it('retries a lost acknowledgement instead of collecting the file again', async () => {
    native.jobs = [job({ stagedPath: '/incoming/i1' })];
    native.digests.set('/incoming/i1', digest);
    native.replies.set('DELETE /v1/outbox/i1', { status: 503, body: '{}' });

    await resumeInbox([receiver]);
    native.replies.set('GET /v1/outbox', {
      status: 200,
      body: listing([{ id: 'i1', filename: 'archive.zip', size: 10, sha256: digest, kind: 'file' }]),
    });

    const first = await checkInbox(receiver);
    expect(first.started).toBe(0);
    expect(ledger.unacked.has('i1')).toBe(true);

    // The receiver answers this time.
    native.replies.delete('DELETE /v1/outbox/i1');
    await checkInbox(receiver);
    expect(ledger.unacked.has('i1')).toBe(false);
    expect(native.started).toEqual([]);
  });

  it('treats a 404 as acknowledged, because that is the state it wanted', async () => {
    native.jobs = [job({ stagedPath: '/incoming/i1' })];
    native.digests.set('/incoming/i1', digest);
    native.replies.set('DELETE /v1/outbox/i1', { status: 404, body: '{}' });

    await resumeInbox([receiver]);
    await checkInbox(receiver);

    expect(ledger.unacked.has('i1')).toBe(false);
  });
});

describe('watching while the app is open', () => {
  // The receivers are read from the keychain after the first render, so a
  // list captured when the listeners were registered is the empty one. A
  // download finishing a moment later would then be reconciled against
  // nobody and left in the container unsaved.
  it('reads the receivers as they are now, not as they were at mount', async () => {
    let receivers: Receiver[] = [];
    const unwatch = watchInbox(
      () => receivers,
      () => {},
    );

    // ...and only now does the app learn which computers it is paired with.
    receivers = [receiver];
    native.jobs = [job({ kind: 'video', stagedPath: '/incoming/i1' })];
    native.digests.set('/incoming/i1', digest);

    listeners.get('onDownloadFinished')?.(native.jobs[0]);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(photos.created).toEqual(['/incoming/i1']);
    unwatch();
  });
});
