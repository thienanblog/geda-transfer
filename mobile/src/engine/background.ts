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

// Handing a transfer to the system.
//
// The foreground engine runs a queue and reacts to what happens. This one gets
// exactly one chance to act: it stages the files, creates the uploads, hands
// them over, and then the app may be swiped away and never run again until the
// transfer is finished. Everything after the hand-over happens in
// `nsurlsessiond` and in the native delegate.
//
// The staging copy is the part that looks wasteful and is not optional. The
// photo library is readable by this app through an entitlement that the system
// process doing the sending does not have, so a background upload can only
// send a file that lives in the app container (see DECISIONS). The copy is
// deleted natively the moment the file lands, including when the app is not
// running.

import { Directory, File, Paths } from 'expo-file-system';

import GedaTransfer, { type BackgroundRequest } from '../../modules/geda-transfer';
import {
  DEFAULT_BUDGET,
  backgroundUploadId,
  landed,
  selectForBackground,
  stagedFileName,
  summarize,
  type BackgroundSummary,
  type StagingBudget,
} from '../core/background';
import { ledgerKey, uploadMetadata } from '../core/plan';
import { mapPool } from '../core/pool';
import type { Asset, Receiver } from '../core/types';
import type { SendOptions } from '../core/selection';
import { recordSentKey, sentKeys } from '../data/ledger';
import { release, resolveAsset, type AssetSummary } from '../media/library';
import { connect } from './session';

export type BackgroundStartOptions = {
  receiver: Receiver;
  summaries: AssetSummary[];
  /** Which parts of each asset to send. Defaults to sending the most. */
  send?: SendOptions;
  budget?: StagingBudget;
};

export type BackgroundStartResult = {
  queued: number;
  /** Left for the next kickoff because the staging budget ran out. */
  deferred: number;
  /** Already on the receiver, according to this phone's ledger. */
  alreadyThere: number;
  errors: string[];
};

/**
 * Stages a batch and hands it to the system.
 *
 * Resolves when the hand-over is complete, which is *not* when the transfer
 * is: that is the entire point. From here the caller can close the app.
 */
export async function startBackgroundTransfer(
  options: BackgroundStartOptions,
): Promise<BackgroundStartResult> {
  const { receiver } = options;
  const errors: string[] = [];

  const baseUrl = await connect(receiver);
  const already = await sentKeys(receiver.deviceId);

  const budget = options.budget ?? DEFAULT_BUDGET;
  const queued = new Set(
    GedaTransfer.backgroundJobs()
      .filter((job) => job.state !== 'done' && job.state !== 'failed')
      .map((job) => job.uploadId),
  );

  // Resolving stops as soon as there are enough candidates to fill the batch.
  //
  // Getting a file path out of a `PHAsset` is the slowest step in a transfer,
  // ahead of the network (AGENTS.md §5), and the budget below is going to
  // discard all but a couple of hundred of them anyway. Resolving a
  // ten-thousand-photo library to stage two hundred files would be minutes of
  // waiting for work that is thrown away.
  const fresh: Asset[] = [];
  let examined = 0;
  let unreadable = 0;

  await mapPool(
    options.summaries,
    async (summary) => {
      // One asset can resolve to several files: a Live Photo is a still and a
      // video, and both have to be queued or the pair arrives as half of one.
      const assets = await resolveAsset(summary, options.send);
      examined += 1;
      for (const asset of assets) {
        if (already.has(ledgerKey(asset))) release(asset);
        else fresh.push(asset);
      }
      return assets;
    },
    {
      shouldContinue: () => fresh.length < budget.maxFiles,
      onError: (error, summary) => {
        examined += 1;
        unreadable += 1;
        note(errors, error, summary.filename);
      },
    },
  );

  const selection = selectForBackground(fresh, {
    budget,
    queued,
    freeBytes: availableSpace(),
  });

  // What this batch had no room for is resolved again on the next kickoff, so
  // the copy made to look at it has no owner. Live Photo videos are tens of
  // megabytes each, and a hundred of them left behind per run is a phone that
  // fills up while backing itself up.
  for (const asset of selection.deferred) release(asset);

  // Staged a few at a time, like the foreground path resolves: the copy is
  // I/O, and doing two hundred of them one after another leaves the app
  // unresponsive for as long as it takes.
  const requests = await mapPool(
    selection.selected,
    async (asset) => {
      const request = await stage(asset, receiver, baseUrl);
      // The staged copy is the one the system will send, so the copy it was
      // made from is finished with. Keeping both would cost twice the disk
      // for every resource that had to be exported.
      release(asset);
      return request;
    },
    {
      onError: (error, asset) => {
        release(asset);
        note(errors, error, asset.filename);
      },
    },
  );

  const started = await GedaTransfer.startBackground(requests);

  // Anything that was staged but not accepted has no owner to delete its
  // copy, so it is cleaned up here rather than left in the container.
  const accepted = new Set(started);
  for (const request of requests) {
    if (!accepted.has(request.uploadId)) discard(request.stagedPath);
  }

  // A wake-up on power and Wi-Fi, for whatever the system has not finished by
  // then and for the files this batch had no room for.
  GedaTransfer.scheduleBackgroundKickoff();

  return {
    queued: started.length,
    // What this batch had no room for, plus everything resolving stopped
    // short of. Both wait for the next kickoff.
    deferred: selection.deferred.length + (options.summaries.length - examined),
    // Counted from the resolve phase alone: `errors` also collects staging
    // failures by the time this is read.
    alreadyThere: examined - fresh.length - unreadable,
    errors,
  };
}

/**
 * Catches the app up with what the system did while it was gone.
 *
 * Called on launch and whenever the app returns to the foreground. Uploads
 * that arrived are written to the ledger and forgotten; uploads the system
 * stopped working on are handed back to it, resuming from the receiver's
 * offset rather than from the beginning. Failures are retried here too: the
 * app being open is decent evidence that the phone is somewhere with a
 * network again.
 */
export async function resumeBackgroundTransfers(): Promise<BackgroundSummary> {
  await GedaTransfer.reconcileBackground();
  await recordArrivals();
  const jobs = await GedaTransfer.retryBackground();
  return summarize(jobs);
}

/**
 * Writes the arrivals to the ledger and lets the native side forget them.
 *
 * Always over the whole current set rather than over one event's job. Two
 * uploads finishing at once would otherwise race: the first event clears
 * every delivered job, including the second one, before it has been written
 * down -- and a file the ledger never heard about is a file sent twice.
 */
export async function recordArrivals(): Promise<void> {
  const arrived = landed(GedaTransfer.backgroundJobs());
  for (const job of arrived) {
    await recordSentKey(job.receiverId, `${job.assetId}:${job.size}`, job.size, job.storedPath);
  }
  if (arrived.length > 0) GedaTransfer.clearDeliveredBackground();
}

export function backgroundSnapshot(): BackgroundSummary {
  return summarize(GedaTransfer.backgroundJobs());
}

/** Drops everything queued, and the staged copies with it. */
export function cancelBackgroundTransfers(): void {
  GedaTransfer.cancelBackground();
}

/**
 * Watches the background session while the app is alive.
 *
 * Progress arrives only while there is a runtime to receive it. The transfer
 * does not depend on anyone listening, so a missed event costs a stale
 * progress bar and nothing else.
 */
export function watchBackground(onChange: (summary: BackgroundSummary) => void): () => void {
  const subscriptions = [
    GedaTransfer.addListener('onBackgroundProgress', () => onChange(backgroundSnapshot())),
    GedaTransfer.addListener('onBackgroundFinished', () => {
      void recordArrivals().finally(() => onChange(backgroundSnapshot()));
    }),
  ];

  return () => {
    for (const subscription of subscriptions) subscription.remove();
  };
}

// MARK: - staging

async function stage(
  asset: Asset,
  receiver: Receiver,
  baseUrl: string,
): Promise<BackgroundRequest> {
  const uploadId = backgroundUploadId(asset);
  const directory = new Directory(GedaTransfer.backgroundStagingDirectory());
  const staged = new File(directory, stagedFileName(uploadId, asset.filename));

  // Leftovers from an attempt that never got as far as being queued, and from
  // a resume that wrote out the remainder of an earlier one. The replacement
  // job records no slice, so nothing else would ever delete that file.
  if (staged.exists) staged.delete();
  discard(`${GedaTransfer.backgroundStagingDirectory()}/${uploadId}.slice`);
  // Awaited: a copy still running when the file is handed over is a task
  // uploading half a photo.
  await new File(asset.filePath).copy(staged);

  return {
    uploadId,
    receiverId: receiver.deviceId,
    receiverName: receiver.name,
    assetId: asset.id,
    filename: asset.filename,
    baseUrl,
    pin: receiver.spki,
    token: receiver.token,
    // A path, not a URI: the native side hands it to URLSession as a file
    // location, and the percent-escapes in a `file://` URI are not part of it.
    stagedPath: decodeURIComponent(staged.uri.replace(/^file:\/\//, '')),
    size: asset.size,
    metadata: uploadMetadata(asset),
  };
}

function discard(path: string): void {
  try {
    const file = new File(path);
    if (file.exists) file.delete();
  } catch {
    // A copy that cannot be deleted is not worth failing a transfer over; the
    // next kickoff overwrites it under the same deterministic name.
  }
}

function availableSpace(): number | undefined {
  try {
    return Paths.availableDiskSpace ?? undefined;
  } catch {
    return undefined;
  }
}

function note(errors: string[], error: unknown, filename?: string): void {
  const base = error instanceof Error ? error.message : String(error);
  const text = filename ? `${filename}: ${base}` : base;
  if (errors.length < 20 && !errors.includes(text)) errors.push(text);
}
