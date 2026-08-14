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

// Deleting what the computer has, once the computer has proved it has it.
//
// The order here is the whole feature, and it is the opposite of the
// convenient one:
//
//   1. hash the bytes this phone actually sent, while it still holds them;
//   2. ask the receiver to reproduce that digest from its own disk, now;
//   3. work out which whole assets are covered by the answers;
//   4. delete those, in one batch, through the system dialog.
//
// Nothing here deletes anything iOS would not put in Recently Deleted, so
// every outcome of every bug in this file is recoverable for thirty days by a
// user who notices. That is the backstop, not the plan.

import { Asset as MediaAsset } from 'expo-media-library';

import GedaTransfer from '../../modules/geda-transfer';
import {
  planDeletions,
  worthAsking,
  type Confirmation,
  type DeletionCandidate,
  type Kept,
} from '../core/deletion';
import type { Asset, Receiver } from '../core/types';
import { ledgerKey } from '../core/plan';
import {
  candidateKeys,
  clearCandidates,
  forgetCandidates,
  pendingCandidates,
  recordCandidate,
} from '../data/deletion';
import { loadDeleteAfterTransfer } from '../data/settings';
import { connect } from './session';

/**
 * How many files one confirmation request asks about.
 *
 * The receiver reads every one of them off its disk in full, so this is far
 * below the dedup probe's thousand (docs/PROTOCOL.md §5.4). Several small
 * requests also mean the answers for the first hundred are usable while the
 * rest are still being read.
 */
const CONFIRM_BATCH = 100;

export type ReclaimOutcome = {
  /** True when the setting is off and nothing was even looked at. */
  off: boolean;
  /** Assets the user was asked about. */
  offered: number;
  /** Assets iOS reported as deleted. Zero when the user said no. */
  deleted: number;
  /** Everything not deleted, with why. */
  kept: Kept[];
  errors: string[];
};

const NOTHING: ReclaimOutcome = { off: true, offered: 0, deleted: 0, kept: [], errors: [] };

/**
 * Records that a file was sent, so it can be asked about later.
 *
 * Called while the file is still on the phone, which is the only moment its
 * digest can be computed -- a staged copy of a Live Photo's video is deleted
 * as soon as the transfer ends.
 *
 * A failure here is not a transfer failure. The worst outcome is an asset
 * that does not get deleted, and that is the direction this whole feature is
 * supposed to fail in.
 */
export async function noteSent(
  receiver: Receiver,
  asset: Asset,
  storedPath: string,
): Promise<void> {
  if (!storedPath) return;

  try {
    // Native, because the bytes may not cross the bridge to be hashed any
    // more than they may to be sent (AGENTS.md §3.8).
    const sha256 = await GedaTransfer.sha256OfFile(asset.filePath);

    await recordCandidate(receiver.deviceId, {
      assetId: asset.id,
      key: ledgerKey(asset),
      storedPath,
      size: asset.size,
      sha256,
      // Undefined when the resolver never established it, and undefined is
      // what blocks the deletion (src/core/deletion.ts).
      withheld: asset.withheld,
      filename: asset.filename,
    });
  } catch {
    // No row, no deletion.
  }
}

/** The keys already recorded, so an unchanged file is not hashed twice. */
export async function alreadyNoted(receiver: Receiver): Promise<Set<string>> {
  try {
    return await candidateKeys(receiver.deviceId);
  } catch {
    return new Set();
  }
}

/**
 * Asks the receiver to vouch for everything waiting, then deletes what it
 * vouched for.
 *
 * The setting is read here rather than taken from the caller: this is the
 * function that destroys things, and it is the right place for the last check
 * that the user asked for it at all.
 */
export async function reclaim(receiver: Receiver): Promise<ReclaimOutcome> {
  if (!(await loadDeleteAfterTransfer())) return NOTHING;

  const candidates = await pendingCandidates(receiver.deviceId);
  if (candidates.length === 0) {
    return { off: false, offered: 0, deleted: 0, kept: [], errors: [] };
  }

  // Filtered before anything is asked. A candidate already disqualified here
  // -- part of its asset never sent, or a row with no digest -- gets the same
  // verdict whatever the receiver says, and every item in a request costs the
  // receiver a full read of the file. Those rows are never cleared, so asking
  // about them would re-read them on every transfer for ever.
  const askable = candidates.filter(worthAsking);
  if (askable.length === 0) {
    // Still planned over the whole list, so the summary can say why each one
    // was kept rather than silently reporting nothing.
    return { off: false, offered: 0, deleted: 0, kept: planDeletions(candidates, []).kept, errors: [] };
  }

  const errors: string[] = [];
  const baseUrl = await connect(receiver);
  const confirmations = await confirmAll(receiver, baseUrl, askable, errors);

  const plan = planDeletions(candidates, confirmations);
  if (plan.deletable.length === 0) {
    return { off: false, offered: 0, deleted: 0, kept: plan.kept, errors };
  }

  // One call for every asset. `Asset.delete` puts the whole batch through a
  // single `PHPhotoLibrary.performChanges`, so iOS shows one confirmation
  // rather than one per photograph (AGENTS.md §3.7) -- a person deleting four
  // hundred files will not read four hundred dialogs, they will learn to tap
  // through them.
  try {
    await MediaAsset.delete(plan.deletable.map((id) => new MediaAsset(id)));
  } catch (error) {
    // Both outcomes arrive here: the user declining the system dialog, and
    // the library refusing. iOS reports the first as a cancellation error and
    // there is no dependable way to tell them apart from JavaScript, so they
    // are not guessed between -- the honest report is that nothing was
    // deleted, plus whatever iOS said. The candidates stay either way, so the
    // next run asks again rather than forgetting these were ever candidates.
    errors.push(`Nothing was deleted: ${message(error)}`);
    return { off: false, offered: plan.deletable.length, deleted: 0, kept: plan.kept, errors };
  }

  const gone = new Set(plan.deletable);
  await clearCandidates(
    receiver.deviceId,
    candidates.filter((c) => gone.has(c.assetId)).map((c) => c.key),
  );

  return {
    off: false,
    offered: plan.deletable.length,
    deleted: plan.deletable.length,
    kept: plan.kept,
    errors,
  };
}

/** Forgets what this phone was going to delete. */
export async function cancelReclaim(receiverId?: string): Promise<void> {
  await forgetCandidates(receiverId);
}

async function confirmAll(
  receiver: Receiver,
  baseUrl: string,
  candidates: DeletionCandidate[],
  errors: string[],
): Promise<Confirmation[]> {
  const out: Confirmation[] = [];

  for (let i = 0; i < candidates.length; i += CONFIRM_BATCH) {
    const batch = candidates.slice(i, i + CONFIRM_BATCH);
    try {
      out.push(...(await confirmBatch(receiver, baseUrl, batch)));
    } catch (error) {
      // A batch that could not be asked about produces no confirmations,
      // which keeps its assets. Deliberately not a rethrow: one unreachable
      // moment must not stop the batches that did get answers.
      errors.push(message(error));
    }
  }
  return out;
}

async function confirmBatch(
  receiver: Receiver,
  baseUrl: string,
  batch: DeletionCandidate[],
): Promise<Confirmation[]> {
  const response = await GedaTransfer.request({
    url: `${baseUrl}/v1/confirm`,
    method: 'POST',
    pin: receiver.spki,
    token: receiver.token,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      items: batch.map((candidate) => ({
        id: candidate.key,
        path: candidate.storedPath,
        size: candidate.size,
        sha256: candidate.sha256,
      })),
    }),
  });

  if (response.status === 401) {
    throw new Error(
      `${receiver.name} no longer recognises this phone. Nothing was deleted. Pair again by scanning its QR code.`,
    );
  }
  if (response.status !== 200) {
    throw new Error(
      `${receiver.name} could not confirm what it holds (HTTP ${response.status}). Nothing was deleted.`,
    );
  }

  return parseConfirmations(response.body);
}

/**
 * Reads the receiver's answers, keeping only the ones that are unambiguously
 * a yes.
 *
 * Anything malformed is dropped rather than guessed at, and a dropped answer
 * is an asset that stays on the phone. There is no shape of broken JSON that
 * should be able to delete a photograph.
 */
export function parseConfirmations(body: string): Confirmation[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return [];
  }

  if (typeof parsed !== 'object' || parsed === null) return [];
  const results = (parsed as { results?: unknown }).results;
  if (!Array.isArray(results)) return [];

  const out: Confirmation[] = [];
  for (const entry of results) {
    if (typeof entry !== 'object' || entry === null) continue;
    const row = entry as { id?: unknown; confirmed?: unknown; reason?: unknown };
    if (typeof row.id !== 'string' || row.id === '') continue;

    out.push({
      key: row.id,
      // Strictly the boolean true. A missing field, a string "true", or a 1
      // are all things a different server might send, and none of them is
      // this receiver saying yes.
      confirmed: row.confirmed === true,
      reason: typeof row.reason === 'string' ? row.reason : undefined,
    });
  }
  return out;
}

function message(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}
