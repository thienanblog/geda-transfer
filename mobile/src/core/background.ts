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

// Deciding what to hand to the background session.
//
// A background transfer is unlike a foreground one in the way that matters
// here: once the batch is handed over, the app may never run again until it is
// finished. There is no loop topping the queue up, no reacting to a file that
// failed, no staging the next photo when the last one lands. Everything the
// system will do has to be decided in one go, while the app is still alive.
//
// That is what makes this file worth testing on its own -- the choices are
// made once and cannot be corrected.

import { orderForThroughput } from './plan';
import type { Asset } from './types';

export type BackgroundJobState = 'pending' | 'running' | 'done' | 'failed';

/** A job as the native side reports it. */
export type BackgroundJob = {
  uploadId: string;
  receiverId: string;
  assetId: string;
  filename: string;
  state: BackgroundJobState;
  size: number;
  /** Against the whole asset, including what an earlier attempt sent. */
  bytesSent: number;
  storedPath: string;
  deduplicated: boolean;
  error: string;
};

export type BackgroundSummary = {
  filesTotal: number;
  filesDone: number;
  filesFailed: number;
  bytesSent: number;
  bytesTotal: number;
  /** Something is still queued or in flight. */
  running: boolean;
  /** Distinct failure messages, capped. */
  errors: string[];
};

export function summarize(jobs: readonly BackgroundJob[]): BackgroundSummary {
  const errors: string[] = [];
  let filesDone = 0;
  let filesFailed = 0;
  let bytesSent = 0;
  let bytesTotal = 0;

  for (const job of jobs) {
    bytesTotal += job.size;
    bytesSent += job.state === 'done' ? job.size : Math.min(job.bytesSent, job.size);

    if (job.state === 'done') filesDone += 1;
    if (job.state === 'failed') {
      filesFailed += 1;
      const text = job.error ? `${job.filename}: ${job.error}` : `${job.filename} failed`;
      // A receiver that has gone away fails every file with the same message;
      // a thousand identical lines help nobody.
      if (errors.length < 20 && !errors.includes(text)) errors.push(text);
    }
  }

  return {
    filesTotal: jobs.length,
    filesDone,
    filesFailed,
    bytesSent,
    bytesTotal,
    running: jobs.some((job) => job.state === 'pending' || job.state === 'running'),
    errors,
  };
}

/**
 * The identifier for one asset going to one receiver.
 *
 * Deterministic on purpose. A kickoff that runs while a job for the same asset
 * is already queued must recognise it rather than send the photo twice, and
 * the app may have been killed and relaunched in between, so the only thing
 * both runs share is the asset itself.
 *
 * The size is part of it for the same reason it is part of the ledger's key:
 * editing a photo keeps its identifier and changes its bytes.
 */
export function backgroundUploadId(asset: Asset): string {
  return sanitize(`${asset.id}-${asset.size}`);
}

/**
 * A filename for the staged copy.
 *
 * `PHAsset.localIdentifier` contains slashes, so it cannot be dropped into a
 * path unescaped -- and the original name cannot be used either, because two
 * photos called `IMG_0001.HEIC` would then be the same staged file.
 */
export function stagedFileName(uploadId: string, filename: string): string {
  const dot = filename.lastIndexOf('.');
  const extension = dot > 0 ? filename.slice(dot + 1).toLowerCase() : 'bin';
  return `${uploadId}.${sanitize(extension)}`;
}

function sanitize(value: string): string {
  return value.replace(/[^A-Za-z0-9._-]/g, '_');
}

/**
 * How much may be staged at once.
 *
 * Staging is a real copy of the file inside the app container -- the system
 * process that does the sending cannot read the photo library (see
 * DECISIONS) -- so the queue costs disk until it drains. These are the limits
 * on that cost, not on the transfer: whatever does not fit is picked up by the
 * next kickoff, once the earlier files have landed and their copies have been
 * deleted.
 */
export type StagingBudget = {
  maxFiles: number;
  maxBytes: number;
};

export const DEFAULT_BUDGET: StagingBudget = {
  maxFiles: 200,
  maxBytes: 4 * 1024 * 1024 * 1024,
};

export type SelectionOptions = {
  budget?: StagingBudget;
  /** Assets already staged, by upload id, so a kickoff does not duplicate. */
  queued?: ReadonlySet<string>;
  /** Free space on the device, when known. */
  freeBytes?: number;
};

export type Selection = {
  selected: Asset[];
  /** Left for the next kickoff, because the budget ran out. */
  deferred: Asset[];
  /** Already queued from an earlier run. */
  skipped: Asset[];
  bytes: number;
};

/**
 * Fills the budget, largest first, skipping what does not fit.
 *
 * Largest first for the reason the foreground plan uses it: a 4 GB video sent
 * last runs on its own after everything else has finished. Skipping rather
 * than stopping at the first item too big to fit is what keeps one enormous
 * video from starving two hundred photos that would all have gone.
 *
 * Half the free space is left alone. Filling a phone's storage with copies of
 * its own photos, while its owner is not looking, is the worst thing this app
 * could do.
 */
export function selectForBackground(assets: Asset[], options: SelectionOptions = {}): Selection {
  const budget = options.budget ?? DEFAULT_BUDGET;
  const queued = options.queued ?? new Set<string>();
  const ceiling =
    options.freeBytes === undefined
      ? budget.maxBytes
      : Math.min(budget.maxBytes, Math.floor(options.freeBytes / 2));

  const selected: Asset[] = [];
  const deferred: Asset[] = [];
  const skipped: Asset[] = [];
  let bytes = 0;

  for (const asset of orderForThroughput(assets)) {
    if (queued.has(backgroundUploadId(asset))) {
      skipped.push(asset);
      continue;
    }
    if (selected.length >= budget.maxFiles || bytes + asset.size > ceiling) {
      deferred.push(asset);
      continue;
    }
    selected.push(asset);
    bytes += asset.size;
  }

  return { selected, deferred, skipped, bytes };
}

/**
 * The jobs whose arrival should be written to the ledger.
 *
 * A deduplicated job counts: the receiver holds the file, which is the only
 * thing the ledger claims. A failed one does not, so the next run sends it
 * again.
 */
export function landed(jobs: readonly BackgroundJob[]): BackgroundJob[] {
  return jobs.filter((job) => job.state === 'done');
}
