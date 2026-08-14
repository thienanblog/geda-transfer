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

import { Directory, File, Paths } from 'expo-file-system';
import {
  Asset as MediaAsset,
  AssetField,
  MediaType,
  Query,
  getPermissionsAsync,
  requestPermissionsAsync,
} from 'expo-media-library';

import GedaTransfer from '../../modules/geda-transfer';
import {
  DEFAULT_SEND_OPTIONS,
  NO_FLAGS,
  chooseResources,
  withheldResources,
  type AssetFlags,
  type Chosen,
  type ResourceType,
  type SendOptions,
} from '../core/selection';
import type { Asset } from '../core/types';

/** An asset as the library lists it, before its file has been located. */
export type AssetSummary = {
  id: string;
  filename: string;
  kind: 'photo' | 'video';
  capturedAt?: number;

  /**
   * Screenshot, hidden, one frame of a burst, edited.
   *
   * Filled in by `listAssets` from the native module -- `expo-media-library`
   * reports none of it, and every one of them is a send option (docs/PLAN.md
   * P8). Defaults to NO_FLAGS so an asset from anywhere else is simply sent.
   *
   * `hasAdjustments` is the exception: knowing it costs a resource lookup per
   * asset, which is the slowest step in a transfer (AGENTS.md §5), so listing
   * leaves it false and `resolveAsset` fills it in from the resource list it
   * fetches anyway.
   */
  flags: AssetFlags;
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

  /**
   * Also list hidden assets, which an ordinary library query never returns.
   *
   * Off by default and separate from the send options on purpose: a listing
   * that quietly contained hidden photos would show them on screen, which is
   * most of the harm before a single byte is sent.
   */
  includeHidden?: boolean;
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

  const summaries: AssetSummary[] = metadata.map((entry) => ({
    id: entry.id,
    filename: entry.filename ?? entry.id,
    kind: entry.mediaType === MediaType.VIDEO ? 'video' : 'photo',
    capturedAt: entry.creationTime ?? entry.modificationTime ?? undefined,
    flags: NO_FLAGS,
  }));

  await attachFlags(summaries);

  if (options.includeHidden) {
    summaries.push(...(await listHidden(options)));
    summaries.sort((a, b) => (b.capturedAt ?? 0) - (a.capturedAt ?? 0));
  }

  return summaries;
}

/**
 * Fills in each summary's flags from the native module.
 *
 * A failure here leaves every asset at NO_FLAGS, which means everything is
 * sent. That is the right way round: a phone whose flags could not be read
 * backs up too much rather than silently missing a photo.
 */
async function attachFlags(summaries: AssetSummary[]): Promise<void> {
  if (summaries.length === 0) return;

  try {
    const flags = await GedaTransfer.assetFlags(summaries.map((s) => s.id));
    const byId = new Map(flags.map((entry) => [entry.id, entry]));
    for (const summary of summaries) {
      const entry = byId.get(summary.id);
      if (entry) summary.flags = entry;
    }
  } catch {
    // Left at NO_FLAGS.
  }
}

async function listHidden(options: ListOptions): Promise<AssetSummary[]> {
  try {
    const hidden = await GedaTransfer.hiddenAssets(options.limit ?? 0);
    const kinds = options.kinds ?? ['photo', 'video'];
    return hidden
      .filter((entry) => kinds.includes(entry.kind))
      .filter((entry) => options.since === undefined || entry.capturedAt > options.since)
      .map((entry) => ({
        id: entry.id,
        filename: entry.filename,
        kind: entry.kind,
        capturedAt: entry.capturedAt || undefined,
        flags: entry,
      }));
  } catch {
    return [];
  }
}

export class AssetUnavailableError extends Error {}

/**
 * Locates the files behind one asset.
 *
 * One asset is not one file. A Live Photo is a still and a video, a ProRAW
 * shot is a negative and often a JPEG, an edited photo is the capture and the
 * render -- and which of those leave the phone is `SendOptions`, decided in
 * `src/core/selection.ts` where it can be tested without a device.
 *
 * An asset that lives only in iCloud is skipped rather than downloaded.
 * Pulling gigabytes down over cellular to push them back up over Wi-Fi is not
 * a transfer the user asked for, and on a metered plan it is an expensive
 * surprise.
 */
export async function resolveAsset(
  summary: AssetSummary,
  options: SendOptions = DEFAULT_SEND_OPTIONS,
): Promise<Asset[]> {
  const asset = new MediaAsset(summary.id);

  if (await asset.getIsInCloud()) {
    throw new AssetUnavailableError(
      `${summary.filename} is in iCloud and not on this device. Download it in Photos first.`,
    );
  }

  const { chosen, flags, withheld } = await choose(summary, options);

  // The resource `getUri` would return is free -- it is a path into the
  // library, not a copy. Everything else has to be written out through
  // PHAssetResourceManager, which is a full copy, and that is the price of
  // the option that asked for it. So the free one stays free even when it is
  // one member of a pair: a Live Photo must not cost a copy of its still as
  // well as its video (docs/PERFORMANCE.md).
  if (chosen.length === 0) return [await fromLibrary(summary, undefined, false, withheld)];

  const paired = chosen.length > 1;
  const out: Asset[] = [];
  for (const resource of chosen) {
    if (!needsExport(summary.kind, flags, resource)) {
      out.push(await fromLibrary(summary, resource, paired, withheld));
    } else {
      out.push(await exportResource(summary, resource, paired, withheld));
    }
  }
  return out;
}

/** The resources this asset should yield, and the flags they were chosen by. */
async function choose(
  summary: AssetSummary,
  options: SendOptions,
): Promise<{ chosen: Chosen[]; flags: AssetFlags; withheld?: ResourceType[] }> {
  try {
    const resources = await GedaTransfer.assetResources(summary.id);
    // Whether the asset has edits is knowable only from its resources, and
    // listing deliberately does not pay for that lookup. It is free here.
    const flags: AssetFlags = {
      ...summary.flags,
      hasAdjustments: resources.some((resource) => resource.type === 'adjustmentData'),
    };
    const chosen = chooseResources(resources, flags, options, summary.kind);
    // `withheldResources` answers undefined when it cannot establish what the
    // asset holds, which is what stops the asset from being deleted.
    return { chosen, flags, withheld: withheldResources(resources, chosen) };
  } catch {
    // No resource list means no choice to make: send the asset as the library
    // presents it, which is what every version before P8 did.
    //
    // `withheld` stays undefined, and that is the point: this branch does not
    // know what the asset holds, so nothing resolved through it may ever be
    // deleted from the phone (src/core/deletion.ts).
    return { chosen: [], flags: summary.flags };
  }
}

/**
 * Whether a chosen resource has to be copied out of the library.
 *
 * `getUri` returns the asset's current representation -- the render when there
 * are edits, the capture when there are not. Anything else has no file URL of
 * its own and can only be reached through PHAssetResourceManager.
 */
function needsExport(
  kind: 'photo' | 'video',
  flags: AssetFlags,
  chosen: Chosen,
): boolean {
  const current = flags.hasAdjustments
    ? kind === 'video'
      ? 'fullSizeVideo'
      : 'fullSizePhoto'
    : kind === 'video'
      ? 'video'
      : 'photo';
  return chosen.type !== current;
}

async function fromLibrary(
  summary: AssetSummary,
  chosen: Chosen | undefined,
  paired = false,
  withheld?: ResourceType[],
): Promise<Asset> {
  const uri = await new MediaAsset(summary.id).getUri();
  const size = new File(uri).size;
  if (size === null || size === undefined) {
    throw new AssetUnavailableError(`${summary.filename} could not be read from the library.`);
  }

  return {
    id: summary.id,
    // The resource's own name when there is one: `IMG_0042.DNG` and
    // `IMG_0042.HEIC` are different files and must not arrive under one name.
    filename: chosen?.filename ?? summary.filename,
    filePath: uri,
    size,
    kind: chosen?.kind ?? summary.kind,
    capturedAt: summary.capturedAt,
    // A lone file carries no pair id: the receiver would otherwise reserve a
    // basename for a pair that has only one member.
    pairId: paired ? summary.id : undefined,
    pairRole: paired ? chosen?.role : undefined,
    resourceType: chosen?.type,
    withheld,
  };
}

async function exportResource(
  summary: AssetSummary,
  chosen: Chosen,
  paired: boolean,
  withheld?: ResourceType[],
): Promise<Asset> {
  const directory = exportDirectory();
  // The asset id and the resource type together: two resources of one asset
  // must not collide, and a re-run must overwrite rather than accumulate.
  const path = `${directory}/${safeName(summary.id)}-${chosen.type}-${safeName(chosen.filename)}`;

  const size = await GedaTransfer.exportResource(summary.id, chosen.type, path);

  return {
    id: summary.id,
    filename: chosen.filename,
    filePath: path,
    size: size || chosen.size,
    kind: chosen.kind,
    capturedAt: summary.capturedAt,
    pairId: paired ? summary.id : undefined,
    pairRole: paired ? chosen.role : undefined,
    resourceType: chosen.type,
    withheld,
    staged: true,
  };
}

/**
 * Deletes a copy this app made, if it made one.
 *
 * Every caller that resolves an asset must call this when it has finished
 * with it. A Live Photo's video is tens of megabytes, and a library import
 * that left one behind per photo would fill the phone.
 */
export function release(asset: Asset): void {
  if (!asset.staged) return;
  try {
    const file = new File(asset.filePath);
    if (file.exists) file.delete();
  } catch {
    // A copy that will not delete is not worth failing a transfer over; the
    // name is deterministic, so the next run overwrites it.
  }
}

/** Where copies of library resources are written. */
function exportDirectory(): string {
  const dir = new Directory(Paths.cache, 'geda-resources');
  // In the cache directory on purpose: iOS may reclaim it under storage
  // pressure, and every file in it is a copy of something the library still
  // holds. Losing one costs a re-export, never a photo.
  if (!dir.exists) dir.create({ intermediates: true });
  return decodeURIComponent(dir.uri.replace(/^file:\/\//, '')).replace(/\/$/, '');
}

function safeName(value: string): string {
  return value.replace(/[^A-Za-z0-9._-]/g, '_');
}
