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

// Deciding what to collect from a receiver, and where to put it.
//
// The desktop cannot push to this phone (AGENTS.md §3.7), so the app asks
// "anything for me?" when it comes to the foreground, and a background session
// carries on after the user puts the phone down. Everything in this file is
// the decision-making half of that, kept free of React Native imports so it
// can be tested without a device.
//
// Two rules decide where a file lands (AGENTS.md §3.7, docs/PROTOCOL.md §6):
//
//   * Photos and videos go to the Photo Library, because that is where their
//     owner will look for them. The naming template does not apply -- iOS does
//     not let an app name anything in the library.
//   * Everything else goes to the app's Files container, always. So do photos
//     and videos when the user has turned on the Advanced setting, which is
//     off by default because ordinary users do not know where those files live
//     or that they take up storage.

/** One file a receiver is offering this phone (docs/PROTOCOL.md §6). */
export type InboxItem = {
  id: string;
  /**
   * The name on the sending computer. **Untrusted**: it crossed a network, and
   * it is not a path until `safeFileName` has had it.
   */
  filename: string;
  size: number;
  /** Hex SHA-256. Nothing is saved before the downloaded bytes match it. */
  sha256: string;
  kind: 'photo' | 'video' | 'file';
  /** RFC3339, when the receiver knew one. */
  capturedAt?: string;
  /** Path on the receiver, relative to its base URL. */
  url: string;
};

export type Destination = 'photos' | 'files';

export type InboxSettings = {
  /**
   * Advanced, default off: put photos and videos in the app's Files container
   * rather than the Photo Library.
   */
  saveMediaToFiles: boolean;
};

export const DEFAULT_INBOX_SETTINGS: InboxSettings = { saveMediaToFiles: false };

/**
 * Where one item is allowed to land.
 *
 * A non-media file has no choice about it: the Photo Library would refuse a
 * ZIP, and asking it is a failure at the very end of a download, after every
 * byte has already been paid for.
 */
export function destinationFor(item: InboxItem, settings: InboxSettings): Destination {
  if (item.kind === 'file') return 'files';
  return settings.saveMediaToFiles ? 'files' : 'photos';
}

/**
 * Parses the receiver's listing, discarding anything unusable.
 *
 * Tolerant on purpose. This is the one place where a document that came off
 * the network is turned into instructions for writing files, so an item
 * missing a digest or a size is dropped rather than half-trusted: without a
 * digest there is nothing to verify against, and verifying is the only thing
 * standing between a corrupted download and the user's photo library.
 */
export function parseOutbox(body: string): InboxItem[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return [];
  }

  const items = (parsed as { items?: unknown })?.items;
  if (!Array.isArray(items)) return [];

  const out: InboxItem[] = [];
  for (const raw of items) {
    const item = coerce(raw);
    if (item) out.push(item);
  }
  return out;
}

function coerce(raw: unknown): InboxItem | undefined {
  if (!raw || typeof raw !== 'object') return undefined;
  const record = raw as Record<string, unknown>;

  const id = typeof record.id === 'string' ? record.id : '';
  const sha256 = typeof record.sha256 === 'string' ? record.sha256 : '';
  const size = typeof record.size === 'number' ? record.size : -1;
  const filename = typeof record.filename === 'string' ? record.filename : '';

  // A hex SHA-256 and nothing else. Anything else is not the digest this was
  // supposed to be, whatever else it might be.
  if (!id || size < 0 || !/^[0-9a-f]{64}$/.test(sha256)) return undefined;

  const kind =
    record.kind === 'photo' || record.kind === 'video' || record.kind === 'file'
      ? record.kind
      : 'file';

  // The receiver names the path, but only ever a path on itself: an item that
  // tried to send this phone somewhere else is answered with the one place it
  // is allowed to point.
  const offered = typeof record.url === 'string' ? record.url : '';
  const url = offered.startsWith('/v1/outbox/') ? offered : `/v1/outbox/${id}`;

  const item: InboxItem = { id, filename, size, sha256, kind, url };
  if (typeof record.captured_at === 'string' && record.captured_at) {
    item.capturedAt = record.captured_at;
  }
  return item;
}

/** What is on offer that this phone has not already dealt with. */
export function toCollect(items: readonly InboxItem[], known: ReadonlySet<string>): InboxItem[] {
  return items.filter((item) => !known.has(item.id));
}

/**
 * Largest first.
 *
 * The same reason the upload plan uses it: a 2 GB archive started last runs on
 * its own after everything else has finished, when there is nothing left to
 * overlap it with.
 */
export function orderForCollection(items: readonly InboxItem[]): InboxItem[] {
  return [...items].sort((a, b) => b.size - a.size);
}

const MAX_NAME = 120;

/**
 * Turns an untrusted name into one that is safe to write.
 *
 * Every part of the name arrived over the network. A receiver is trusted with
 * the *bytes* -- the pin and the digest see to that -- but a name is not
 * bytes, and `../../Library/Preferences/x.plist` is a perfectly ordinary
 * string. Separators, traversal, control characters, and leading dots all go;
 * what is left is a plain name with its extension intact.
 */
export function safeFileName(name: string): string {
  // Only the last component, whichever kind of separator was used to build it.
  const base = name.split(/[/\\]/).pop() ?? '';

  const cleaned = base
    // Control characters go: a NUL truncates a C string, and a newline
    // would let a filename forge a second line of anything that logs it.
    .replace(/[\u0000-\u001f\u007f]/g, '')
    .replace(/[:*?"<>|]/g, '_')
    .replace(/^\.+/, '')
    .trim();

  if (cleaned === '') return 'file';
  if (cleaned.length <= MAX_NAME) return cleaned;

  // Truncate the stem, never the extension: the extension is what decides
  // which app opens it.
  const dot = cleaned.lastIndexOf('.');
  if (dot <= 0 || cleaned.length - dot > 12) return cleaned.slice(0, MAX_NAME);

  const extension = cleaned.slice(dot);
  return cleaned.slice(0, MAX_NAME - extension.length) + extension;
}

/**
 * A name nothing else in the folder is using.
 *
 * The same collision rule as the receiver's (AGENTS.md §3.6), minus the hash
 * comparison: two files with one name get `_1`, `_2`, and so on, rather than
 * one quietly replacing the other.
 */
export function uniqueName(name: string, taken: ReadonlySet<string>): string {
  if (!taken.has(name)) return name;

  const dot = name.lastIndexOf('.');
  const stem = dot > 0 ? name.slice(0, dot) : name;
  const extension = dot > 0 ? name.slice(dot) : '';

  for (let counter = 1; counter < 10_000; counter++) {
    const candidate = `${stem}_${counter}${extension}`;
    if (!taken.has(candidate)) return candidate;
  }
  throw new Error(`no free name for ${name}`);
}

// MARK: - what the native session reports

export type DownloadState = 'pending' | 'running' | 'ready' | 'saved' | 'failed';

/** One download, as the native side reports it. */
export type DownloadJob = {
  itemId: string;
  receiverId: string;
  receiverName: string;
  filename: string;
  kind: 'photo' | 'video' | 'file';
  capturedAt: string;
  sha256: string;
  size: number;
  bytesReceived: number;
  state: DownloadState;
  /** Where the finished file is waiting to be verified and saved. */
  stagedPath: string;
  error: string;
};

export type InboxSummary = {
  filesTotal: number;
  filesDone: number;
  filesFailed: number;
  bytesReceived: number;
  bytesTotal: number;
  /** Something is still downloading, or downloaded and not yet saved. */
  running: boolean;
  errors: string[];
};

export function summarizeInbox(jobs: readonly DownloadJob[]): InboxSummary {
  const errors: string[] = [];
  let filesDone = 0;
  let filesFailed = 0;
  let bytesReceived = 0;
  let bytesTotal = 0;

  for (const job of jobs) {
    bytesTotal += job.size;
    bytesReceived += job.state === 'saved' ? job.size : Math.min(job.bytesReceived, job.size);

    if (job.state === 'saved') filesDone += 1;
    if (job.state === 'failed') {
      filesFailed += 1;
      const text = job.error ? `${job.filename}: ${job.error}` : `${job.filename} failed`;
      // One receiver that has gone away fails every file with the same
      // message; a hundred identical lines help nobody.
      if (errors.length < 20 && !errors.includes(text)) errors.push(text);
    }
  }

  return {
    filesTotal: jobs.length,
    filesDone,
    filesFailed,
    bytesReceived,
    bytesTotal,
    // A file on disk that has not been saved yet is still work outstanding:
    // saving needs the app, which is exactly what the user has just opened.
    running: jobs.some(
      (job) => job.state === 'pending' || job.state === 'running' || job.state === 'ready',
    ),
    errors,
  };
}

/** The downloads that are on disk and waiting for the app to verify them. */
export function awaitingSave(jobs: readonly DownloadJob[]): DownloadJob[] {
  return jobs.filter((job) => job.state === 'ready' && job.stagedPath !== '');
}
