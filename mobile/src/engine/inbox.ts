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

// Collecting what a computer has queued for this phone.
//
// The whole direction is a pull. Nothing can wake a suspended iOS app to hand
// it a file (AGENTS.md §3.7), so the app asks "anything for me?" when it comes
// to the foreground, hands what it finds to a background session, and the
// system carries on after the user puts the phone down.
//
// The order of the last three steps is the part worth being careful about:
//
//   1. verify the bytes against the digest the receiver published,
//   2. save the file, and write down that it was saved,
//   3. only then tell the receiver.
//
// Saving before verifying would put corrupted files in somebody's photo
// library. Acknowledging before writing the ledger row would let a crash in
// between produce a second copy of the same video, because the receiver would
// have retired the item while this phone had no memory of it.

import { Directory, File } from 'expo-file-system';
import {
  Asset as MediaAsset,
  getPermissionsAsync,
  requestPermissionsAsync,
} from 'expo-media-library';

import GedaTransfer, { type DownloadRequest } from '../../modules/geda-transfer';
import {
  DEFAULT_INBOX_SETTINGS,
  awaitingSave,
  destinationFor,
  orderForCollection,
  parseOutbox,
  safeFileName,
  summarizeInbox,
  toCollect,
  uniqueName,
  type Destination,
  type DownloadJob,
  type InboxItem,
  type InboxSettings,
  type InboxSummary,
} from '../core/inbox';
import type { Receiver } from '../core/types';
import { markAcknowledged, receivedIds, recordReceived, unacknowledged } from '../data/inbox';
import { loadInboxSettings } from '../data/settings';
import { connect } from './session';

export type CheckResult = {
  /** Handed to the system on this run. */
  started: number;
  /** On offer but already collected before. */
  alreadyHere: number;
  /** Verified and saved on this run, including from earlier downloads. */
  saved: number;
  errors: string[];
};

/**
 * Asks one receiver what is waiting, and starts collecting it.
 *
 * Returns as soon as the hand-over is done, which is not when the files have
 * arrived: that is the point of a background session. Anything already
 * downloaded is verified and saved first, because the user opening the app is
 * the only chance a file has of reaching the photo library.
 */
export async function checkInbox(receiver: Receiver): Promise<CheckResult> {
  const errors: string[] = [];

  // Downloads that finished while the app was closed. Saving needs no
  // network, so it happens before the receiver is contacted at all: a file
  // already on the phone must reach the photo library even if the computer it
  // came from has since been closed.
  const saved = await saveArrivals(receiver, errors);

  const baseUrl = await connect(receiver);

  // Acknowledgements that never got through, including the ones just above.
  // Until one lands the receiver still has the item on offer, and this is
  // what stops the same video being collected a second time.
  await sendAcknowledgements(receiver, baseUrl, errors);

  const response = await GedaTransfer.request({
    url: `${baseUrl}/v1/outbox`,
    method: 'GET',
    pin: receiver.spki,
    token: receiver.token,
  });

  if (response.status === 401) {
    throw new Error(
      `${receiver.name} no longer recognises this phone. Pair again by scanning its QR code.`,
    );
  }
  if (response.status !== 200) {
    throw new Error(`${receiver.name} could not be asked what it has (HTTP ${response.status}).`);
  }

  const offered = parseOutbox(response.body);
  const known = await receivedIds(receiver.deviceId);
  const wanted = orderForCollection(toCollect(offered, known));

  const started = await GedaTransfer.startDownloads(
    wanted.map((item) => request(item, receiver, baseUrl)),
  );

  // A wake-up on power and Wi-Fi, for whatever the system has not finished by
  // then and for the files that still need saving afterwards.
  GedaTransfer.scheduleBackgroundKickoff();

  return {
    started: started.length,
    alreadyHere: offered.length - wanted.length,
    saved,
    errors,
  };
}

function request(item: InboxItem, receiver: Receiver, baseUrl: string): DownloadRequest {
  return {
    itemId: item.id,
    receiverId: receiver.deviceId,
    receiverName: receiver.name,
    filename: item.filename,
    kind: item.kind,
    capturedAt: item.capturedAt ?? '',
    baseUrl,
    path: item.url,
    pin: receiver.spki,
    token: receiver.token,
    sha256: item.sha256,
    size: item.size,
  };
}

/**
 * Catches the app up with what the system downloaded while it was gone.
 *
 * Called on launch and whenever the app returns to the foreground, exactly
 * like the upload side's reconcile. Saving is the part only the app can do:
 * writing to the photo library needs PhotoKit, which does not exist in a
 * process the system launched purely to deliver a completion.
 */
export async function resumeInbox(receivers: readonly Receiver[]): Promise<InboxSummary> {
  await GedaTransfer.reconcileDownloads();

  const errors: string[] = [];
  for (const receiver of receivers) {
    await saveArrivals(receiver, errors);
  }

  const jobs = await GedaTransfer.retryDownloads();
  return summarizeInbox(jobs);
}

export function inboxSnapshot(): InboxSummary {
  return summarizeInbox(GedaTransfer.downloadJobs());
}

/** Drops everything queued, and the downloaded copies with it. */
export function cancelInbox(): void {
  GedaTransfer.cancelDownloads();
}

/**
 * Watches the download session while the app is alive.
 *
 * The transfer does not depend on anyone listening, so a missed event costs a
 * stale progress bar and nothing else.
 */
export function watchInbox(
  receivers: readonly Receiver[],
  onChange: (summary: InboxSummary) => void,
): () => void {
  const subscriptions = [
    GedaTransfer.addListener('onDownloadProgress', () => onChange(inboxSnapshot())),
    GedaTransfer.addListener('onDownloadFinished', () => {
      // A finished download is a file on disk that nobody has verified yet,
      // and the app is alive right now, which is the only time that can
      // happen.
      void resumeInbox(receivers)
        .then(onChange)
        .catch(() => onChange(inboxSnapshot()));
    }),
  ];

  return () => {
    for (const subscription of subscriptions) subscription.remove();
  };
}

// MARK: - verifying and saving

/**
 * Verifies and saves everything the system has finished downloading.
 *
 * Returns how many files were saved. Errors are collected rather than thrown:
 * one file the photo library refuses must not stop the other nine.
 */
async function saveArrivals(receiver: Receiver, errors: string[]): Promise<number> {
  const jobs = awaitingSave(GedaTransfer.downloadJobs()).filter(
    (job) => job.receiverId === receiver.deviceId,
  );
  if (jobs.length === 0) return 0;

  const settings = await settingsOrDefault();
  let saved = 0;

  for (const job of jobs) {
    try {
      await save(job, settings);
      saved += 1;
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error);
      // The bytes go with the failure: a file whose digest did not match is
      // not something to keep, and certainly not something to save.
      GedaTransfer.failDownload(job.itemId, reason);
      note(errors, `${job.filename}: ${reason}`);
    }
  }

  return saved;
}

async function save(job: DownloadJob, settings: InboxSettings): Promise<void> {
  const item: InboxItem = {
    id: job.itemId,
    filename: job.filename,
    size: job.size,
    sha256: job.sha256,
    kind: job.kind,
    url: '',
  };

  // Before anything else touches these bytes. The receiver was authenticated
  // by its pinned key, so this is not about trusting it -- it is about the
  // network, the disk, and a resume that spliced the wrong tail on.
  const digest = await GedaTransfer.sha256OfFile(job.stagedPath);
  if (digest !== job.sha256) {
    throw new Error('the file arrived damaged and was not saved');
  }

  const destination = destinationFor(item, settings);
  if (destination === 'photos') {
    await saveToPhotos(job);
  } else {
    await saveToFiles(job);
  }

  // Written before the receiver is told -- and the receiver is told later,
  // over a connection this function does not need. A crash in between leaves
  // a phone that knows it has the file rather than one that collects it
  // twice, which is the failure worth designing against.
  await recordReceived(job.receiverId, item, destination);

  // Only now: the staged copy is what a retry would have used.
  GedaTransfer.finishDownload(job.itemId);
}

/**
 * Into the Photo Library, which is where a photo's owner will look for it.
 *
 * The naming template does not apply here and cannot: iOS does not let an app
 * name anything in the library (AGENTS.md §3.7). The file keeps its capture
 * date instead, which is what decides where it appears in the timeline.
 */
async function saveToPhotos(job: DownloadJob): Promise<void> {
  if (!(await canWriteToLibrary())) {
    throw new Error(
      'this app cannot add to your photo library. Allow it in Settings, or turn on "Save to Files instead".',
    );
  }
  await MediaAsset.create(job.stagedPath);
}

/**
 * Whether the library will accept a write.
 *
 * "Selected photos" is enough: adding an asset does not need to see the ones
 * already there. Asking is worth it -- the alternative is a failure at the end
 * of a download with nothing a person can act on.
 */
async function canWriteToLibrary(): Promise<boolean> {
  const current = await getPermissionsAsync(true);
  if (current.granted) return true;
  if (!current.canAskAgain) return false;
  return (await requestPermissionsAsync(true)).granted;
}

/**
 * Into the app's Files container, under Received/.
 *
 * Everything that is not a photo or a video goes here, always: the Photo
 * Library would refuse a ZIP, and finding that out at the end of a 2 GB
 * download is the expensive way to learn it.
 */
async function saveToFiles(job: DownloadJob): Promise<void> {
  const directory = new Directory(GedaTransfer.receivedDirectory());
  const taken = new Set(existingNames(directory));
  const name = uniqueName(safeFileName(job.filename), taken);

  await new File(job.stagedPath).move(new File(directory, name));
}

function existingNames(directory: Directory): string[] {
  try {
    return directory.list().map((entry) => entry.name);
  } catch {
    // A directory that cannot be listed is one where nothing can collide
    // either; the move below reports the real problem if there is one.
    return [];
  }
}

/**
 * Tells the receiver the file arrived and its digest matched.
 *
 * A failure here is deliberately not a failure of the save: the file is on the
 * phone, the ledger says so, and the next check retries the acknowledgement.
 * The alternative -- treating it as an error -- would discard a file that
 * arrived perfectly well because a Wi-Fi network dropped a moment later.
 */
async function acknowledge(
  receiverId: string,
  itemId: string,
  baseUrl: string,
  pin: string,
  token: string,
): Promise<void> {
  try {
    const response = await GedaTransfer.request({
      url: `${baseUrl}/v1/outbox/${itemId}`,
      method: 'DELETE',
      pin,
      token,
    });
    // 404 counts as success: the receiver has already forgotten the item,
    // which is the state the acknowledgement was trying to produce.
    if ((response.status >= 200 && response.status < 300) || response.status === 404) {
      await markAcknowledged(receiverId, itemId);
    }
  } catch {
    // Retried on the next check.
  }
}

/** Sends every acknowledgement this phone still owes one receiver. */
async function sendAcknowledgements(
  receiver: Receiver,
  baseUrl: string,
  errors: string[],
): Promise<void> {
  try {
    for (const itemId of await unacknowledged(receiver.deviceId)) {
      await acknowledge(receiver.deviceId, itemId, baseUrl, receiver.spki, receiver.token);
    }
  } catch (error) {
    note(errors, error instanceof Error ? error.message : String(error));
  }
}

async function settingsOrDefault(): Promise<InboxSettings> {
  try {
    return await loadInboxSettings();
  } catch {
    return DEFAULT_INBOX_SETTINGS;
  }
}

function note(errors: string[], text: string): void {
  if (errors.length < 20 && !errors.includes(text)) errors.push(text);
}

export type { Destination };
