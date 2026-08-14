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

import type { ResourceType } from './selection';

/** A receiver this phone has paired with. */
export type Receiver = {
  deviceId: string;
  name: string;
  /** base64(SHA-256(SubjectPublicKeyInfo)); the whole of the trust relationship. */
  spki: string;
  /** Every address the receiver advertised, `host:port`. Raced on connect. */
  addrs: string[];
  /** Per-device bearer token issued at pairing. */
  token: string;
  pairedAt: number;
  /** The address that worked last time, tried first on the next connect. */
  lastGoodAddr?: string;
};

/** One asset selected for sending. */
export type Asset = {
  /** `PHAsset.localIdentifier` on iOS. Stable, and the key of the local ledger. */
  id: string;
  filename: string;
  /** A path on disk. The original resource, never a transcode (AGENTS.md §3.3). */
  filePath: string;
  size: number;
  kind: 'photo' | 'video' | 'file';
  /** Milliseconds. The asset's capture date, not the transfer time. */
  capturedAt?: number;
  /**
   * Groups the members of a Live Photo or a RAW+JPEG pair so the receiver can
   * give them one basename (AGENTS.md §3.6).
   */
  pairId?: string;

  /**
   * Which member of the pair this is. The still is primary; the motion, the
   * JPEG beside a negative, and the untouched original are secondary.
   *
   * It cannot be derived from `kind`: a RAW+JPEG pair is two photos, and an
   * edited photo sent with its original is two photos as well.
   */
  pairRole?: 'primary' | 'secondary';

  /** Which `PHAssetResource` this came from. Empty for a plain file. */
  resourceType?: ResourceType;

  /**
   * Photographic resources of this asset that were NOT sent.
   *
   * Only delete-after-transfer reads it, and it is the difference between
   * deleting a photograph and deleting a copy of one: an asset whose negative
   * or whose untouched capture stayed behind must not be removed from the
   * phone, however firmly the receiver vouches for what it did get.
   *
   * Undefined means the question was never asked, and blocks deletion exactly
   * as a non-empty list does.
   */
  withheld?: ResourceType[];

  /**
   * True when `filePath` is a copy this app made and owns.
   *
   * A resource with no file URL behind it -- a Live Photo's video, a raw
   * negative's JPEG -- has to be written out of the library before it can be
   * sent, and whoever asked for it has to delete it afterwards.
   */
  staged?: boolean;

  album?: string;
};

export type TransferItemState =
  | 'queued'
  | 'uploading'
  | 'done'
  | 'skipped'
  | 'failed'
  | 'cancelled';

export type TransferItem = {
  asset: Asset;
  state: TransferItemState;
  bytesSent: number;
  /** Set once the receiver has committed the file. */
  storedPath?: string;
  /** The receiver already had this file. */
  deduplicated?: boolean;
  error?: string;
  /** The tus upload URL, kept so an interrupted transfer resumes. */
  location?: string;
};
