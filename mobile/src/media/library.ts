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

// Reading the photo library.
//
// Two rules shape this file:
//
//   * Originals only. The phone never transcodes -- HEIC stays HEIC, and the
//     conversion, if the user wants one at all, happens on a real CPU at the
//     other end (AGENTS.md §3.3).
//   * Listing is cheap, resolving is not. Getting a file path out of a PHAsset
//     is the slowest step in the whole transfer, ahead of the network
//     (AGENTS.md §5), so the list is built from metadata the media store keeps
//     in memory, and the path is resolved later, overlapped with uploading.

import { File } from 'expo-file-system';
import {
  Asset as MediaAsset,
  AssetField,
  MediaType,
  Query,
  getPermissionsAsync,
  requestPermissionsAsync,
} from 'expo-media-library';

import type { Asset } from '../core/types';

/** An asset as the library lists it, before its file has been located. */
export type AssetSummary = {
  id: string;
  filename: string;
  kind: 'photo' | 'video';
  capturedAt?: number;
};

export type LibraryAccess = 'granted' | 'limited' | 'denied';

export async function checkAccess(): Promise<LibraryAccess> {
  return classify(await getPermissionsAsync());
}

export async function requestAccess(): Promise<LibraryAccess> {
  return classify(await requestPermissionsAsync());
}

function classify(permission: { granted: boolean; accessPrivileges?: string }): LibraryAccess {
  if (!permission.granted) return 'denied';
  // "Selected photos" is a normal choice, not an error. The app shows what it
  // can see and says so, rather than nagging.
  return permission.accessPrivileges === 'limited' ? 'limited' : 'granted';
}

export type ListOptions = {
  /** Newest first, capped. */
  limit?: number;
  /** Only assets created after this millisecond timestamp. */
  since?: number;
  kinds?: ('photo' | 'video')[];
};

/**
 * Lists assets newest first.
 *
 * `exeForMetadata` deliberately: it reads what the media store already has and
 * never resolves a file path, which is what keeps listing ten thousand photos
 * a fraction of a second rather than a minute.
 */
export async function listAssets(options: ListOptions = {}): Promise<AssetSummary[]> {
  const kinds = options.kinds ?? ['photo', 'video'];
  const wanted = kinds.map((kind) => (kind === 'photo' ? MediaType.IMAGE : MediaType.VIDEO));

  let query = new Query()
    .within(AssetField.MEDIA_TYPE, wanted)
    .orderBy({ key: AssetField.CREATION_TIME, ascending: false });

  if (options.since !== undefined) {
    query = query.gt(AssetField.CREATION_TIME, options.since);
  }
  if (options.limit !== undefined) {
    query = query.limit(options.limit);
  }

  const metadata = await query.exeForMetadata();

  return metadata.map((entry) => ({
    id: entry.id,
    filename: entry.filename ?? entry.id,
    kind: entry.mediaType === MediaType.VIDEO ? 'video' : 'photo',
    capturedAt: entry.creationTime ?? entry.modificationTime ?? undefined,
  }));
}

export class AssetUnavailableError extends Error {}

/**
 * Locates an asset's original file and its size.
 *
 * An asset that lives only in iCloud is skipped rather than downloaded.
 * Pulling gigabytes down over cellular to push them back up over Wi-Fi is not
 * a transfer the user asked for, and on a metered plan it is an expensive
 * surprise.
 */
export async function resolveAsset(summary: AssetSummary): Promise<Asset> {
  const asset = new MediaAsset(summary.id);

  if (await asset.getIsInCloud()) {
    throw new AssetUnavailableError(
      `${summary.filename} is in iCloud and not on this device. Download it in Photos first.`,
    );
  }

  const uri = await asset.getUri();
  const file = new File(uri);
  const size = file.size;
  if (size === null || size === undefined) {
    throw new AssetUnavailableError(`${summary.filename} could not be read from the library.`);
  }

  return {
    id: summary.id,
    filename: summary.filename,
    filePath: uri,
    size,
    kind: summary.kind,
    capturedAt: summary.capturedAt,
  };
}
