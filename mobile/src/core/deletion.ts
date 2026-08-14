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

// Deciding what may be deleted, which is the only irreversible thing this app
// does to somebody's own library.
//
// Everything else here fails towards spending bandwidth: a wrong dedup answer
// re-sends a file, a lost ledger row re-sends a library. This decision fails
// towards a photograph that no longer exists anywhere. So the whole module is
// written the other way round from the rest of the app -- nothing is
// deletable until several independent things all say so, and every unknown,
// every missing answer, and every unparsed reply means keep.
//
// It is plain TypeScript with no React Native imports, so the rules are tested
// without a device. That is deliberate: this is the last place in the codebase
// that should be finding out what it does on a phone.
//
// Two conditions have to hold for an asset to go. They are different in kind
// and neither implies the other:
//
//   1. The receiver has proved, just now, that it can still produce the bytes
//      of every file this asset was sent as (docs/PROTOCOL.md §5.4).
//   2. Every photographic resource of the asset was actually sent. An asset
//      is not a file: sending the rendered half of an edited photo and then
//      deleting it destroys the untouched capture, which never left the phone.

import type { ResourceType } from './selection';

/** One file that was sent, and which the receiver may be asked to vouch for. */
export type DeletionCandidate = {
  /**
   * `PHAsset.localIdentifier`. Several candidates share it -- a Live Photo is
   * one asset and two files -- and it is what iOS deletes by.
   */
  assetId: string;

  /** The local ledger key. Unique per resource, and what confirmations quote. */
  key: string;

  /** Where the receiver said it put the file. Its key for the confirmation. */
  storedPath: string;

  size: number;

  /** Hex SHA-256, computed on this phone over the bytes that were sent. */
  sha256: string;

  /**
   * Photographic resources of this asset that were *not* sent.
   *
   * `undefined` means the question was never answered, which blocks deletion
   * exactly as a non-empty list does. An unknown is not a no.
   */
  withheld?: ResourceType[];

  /** For the summary on screen. */
  filename: string;
};

/** What the receiver answered about one candidate. */
export type Confirmation = {
  key: string;
  confirmed: boolean;
  /** Empty when confirmed; one of the receiver's reasons otherwise. */
  reason?: string;
};

/** Why an asset was not deleted. */
export type KeptReason =
  /** The receiver did not vouch for at least one of its files. */
  | 'unconfirmed'
  /** Part of the asset was never sent, so deleting it would lose that part. */
  | 'withheld'
  /** No answer arrived for at least one of its files. */
  | 'unanswered';

export type Kept = {
  assetId: string;
  filename: string;
  reason: KeptReason;
  /** The receiver's own word for what was wrong, when it gave one. */
  detail?: string;
};

export type DeletionPlan = {
  /** Asset ids that may be deleted. Every file of every one is accounted for. */
  deletable: string[];
  /** Everything else, with why. Nothing is dropped silently. */
  kept: Kept[];
};

/**
 * Turns candidates plus the receiver's answers into what may be deleted.
 *
 * The default for anything not explicitly confirmed is keep. That is not
 * defensive style for its own sake -- the failure this guards against is a
 * confirmation that never arrived being read as a yes, which is precisely
 * what a dropped connection, a truncated response, or a receiver that
 * answered a different question all look like from here.
 */
export function planDeletions(
  candidates: DeletionCandidate[],
  confirmations: Confirmation[],
): DeletionPlan {
  const answers = new Map<string, Confirmation>();
  for (const confirmation of confirmations) {
    // Last write wins, but a refusal is never overwritten by a confirmation:
    // a receiver that answered twice about one file has already said the
    // thing that matters.
    const existing = answers.get(confirmation.key);
    if (existing && !existing.confirmed) continue;
    answers.set(confirmation.key, confirmation);
  }

  const byAsset = new Map<string, DeletionCandidate[]>();
  for (const candidate of candidates) {
    const group = byAsset.get(candidate.assetId);
    if (group) group.push(candidate);
    else byAsset.set(candidate.assetId, [candidate]);
  }

  const deletable: string[] = [];
  const kept: Kept[] = [];

  for (const [assetId, group] of byAsset) {
    const verdict = verdictFor(group, answers);
    if (verdict === null) {
      deletable.push(assetId);
    } else {
      kept.push({ assetId, filename: group[0].filename, ...verdict });
    }
  }

  return { deletable, kept };
}

/** The reason to keep an asset, or null when there is none. */
function verdictFor(
  group: DeletionCandidate[],
  answers: Map<string, Confirmation>,
): Omit<Kept, 'assetId' | 'filename'> | null {
  // Checked first, and before any answer is consulted: an asset that was only
  // partly sent cannot be deleted no matter how firmly the receiver vouches
  // for the part it got.
  for (const candidate of group) {
    if (candidate.withheld === undefined) {
      return { reason: 'withheld', detail: 'it is not known what else this asset holds' };
    }
    if (candidate.withheld.length > 0) {
      return { reason: 'withheld', detail: candidate.withheld.join(', ') };
    }
  }

  for (const candidate of group) {
    if (!usable(candidate)) {
      return { reason: 'unconfirmed', detail: 'nothing was recorded about this file' };
    }

    const answer = answers.get(candidate.key);
    if (!answer) return { reason: 'unanswered' };
    if (!answer.confirmed) return { reason: 'unconfirmed', detail: answer.reason };
  }

  return null;
}

/**
 * Whether asking the receiver about this candidate could change anything.
 *
 * A candidate that is already disqualified locally -- part of its asset was
 * never sent, or the row is missing a digest -- gets the same verdict however
 * the receiver answers. Asking anyway is not merely wasted: every item in a
 * confirmation request costs the receiver a full read of the file
 * (docs/PROTOCOL.md §5.4), and these candidates are never cleared, so a
 * library of edited photos would re-read every one of them on every transfer,
 * for answers that are then discarded.
 *
 * Deliberately built from the same two predicates `verdictFor` uses, so a
 * change to eligibility cannot make this filter start hiding askable files.
 */
export function worthAsking(candidate: DeletionCandidate): boolean {
  return candidate.withheld !== undefined && candidate.withheld.length === 0 && usable(candidate);
}

/**
 * Whether a candidate carries enough to have been confirmed at all.
 *
 * A confirmation quotes a path, a size and a digest. One of them missing
 * means the receiver was asked a question with a hole in it, and an answer to
 * that question authorises nothing -- however cheerful it was.
 */
function usable(candidate: DeletionCandidate): boolean {
  return (
    candidate.storedPath.length > 0 &&
    candidate.size >= 0 &&
    /^[0-9a-f]{64}$/i.test(candidate.sha256)
  );
}

/** A line of text for the summary, or "" when nothing was kept. */
export function describeKept(kept: Kept[]): string {
  if (kept.length === 0) return '';

  const counts = { withheld: 0, unconfirmed: 0, unanswered: 0 };
  for (const item of kept) counts[item.reason] += 1;

  const parts: string[] = [];
  if (counts.withheld > 0) {
    parts.push(`${counts.withheld} because only part of them was sent`);
  }
  if (counts.unconfirmed > 0) {
    parts.push(`${counts.unconfirmed} the computer could not vouch for`);
  }
  if (counts.unanswered > 0) {
    parts.push(`${counts.unanswered} the computer did not answer about`);
  }

  const noun = kept.length === 1 ? 'item was' : 'items were';
  return `${kept.length} ${noun} kept on this phone: ${parts.join('; ')}.`;
}
