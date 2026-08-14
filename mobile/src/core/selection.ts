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

// What to send, and which parts of it.
//
// One photo in the library is not one file. A Live Photo is a still and a
// video; a ProRAW shot is a negative and often a rendered JPEG beside it; an
// edited photo is the untouched original plus the render the user actually
// sees. iOS models all of these as one `PHAsset` with several
// `PHAssetResource`s (docs/PROTOCOL.md §5.1), and this file decides which of
// those resources leave the phone.
//
// It is plain TypeScript with no React Native imports, so every rule here is
// tested without a device -- which matters, because the rules are where a
// backup quietly loses somebody's negatives.

/** A `PHAssetResource` type, as the native module reports it. */
export type ResourceType =
  | 'photo'
  | 'fullSizePhoto'
  | 'alternatePhoto'
  | 'video'
  | 'fullSizeVideo'
  | 'pairedVideo'
  | 'fullSizePairedVideo'
  | 'audio'
  | 'photoProxy'
  | 'adjustmentData'
  | 'adjustmentBasePhoto'
  | 'adjustmentBaseVideo'
  | 'adjustmentBasePairedVideo'
  | 'unknown';

/** One resource of an asset, before anything has been read from disk. */
export type Resource = {
  type: ResourceType;
  /** The name iOS recorded for it. Becomes `filename` in the tus metadata. */
  filename: string;
  /** Uniform Type Identifier, e.g. `public.heic` or `com.adobe.raw-image`. */
  uti: string;
  /** Bytes, or 0 when iOS did not say. */
  size: number;
};

/** The flags that decide whether an asset is sent at all. */
export type AssetFlags = {
  isScreenshot: boolean;
  isHidden: boolean;
  /** Non-empty when the asset is one frame of a burst. */
  burstId: string;
  /** The frame iOS picked to stand for the burst. */
  representsBurst: boolean;
  /** The user marked this frame as one to keep. */
  userPickedFromBurst: boolean;
  /** The asset has edits, so a rendered version exists beside the original. */
  hasAdjustments: boolean;
};

export const NO_FLAGS: AssetFlags = {
  isScreenshot: false,
  isHidden: false,
  burstId: '',
  representsBurst: false,
  userPickedFromBurst: false,
  hasAdjustments: false,
};

/**
 * What the user chose to send.
 *
 * Every default sends *more* rather than less, with one exception. The point
 * of a backup is that the thing you did not think about is in it, so the
 * motion of a Live Photo, the negative of a ProRAW shot, and screenshots are
 * all on. Hidden assets are the exception: somebody hid those on purpose, and
 * a backup that quietly un-hides them onto a shared family computer is a
 * different kind of data loss.
 */
export type SendOptions = {
  /** Send the motion half of a Live Photo, so it stays a Live Photo. */
  livePhotoVideo: boolean;

  /**
   * Which version of an edited photo to send.
   *
   * `edited` is what the user sees in Photos, and is what almost everybody
   * means by "my photo". `original` is the untouched capture. `both` sends the
   * two as a pair, which is the only option that cannot lose anything.
   */
  edits: 'edited' | 'original' | 'both';

  /** For RAW+JPEG shots: the negative, the rendered JPEG, or both. */
  raw: 'both' | 'raw' | 'jpeg';

  /** Every frame of a burst, or the ones iOS and the user picked. */
  bursts: 'picks' | 'all';

  screenshots: boolean;

  /** Hidden assets. Off, and the only default that sends less. */
  hidden: boolean;
};

export const DEFAULT_SEND_OPTIONS: SendOptions = {
  livePhotoVideo: true,
  edits: 'edited',
  raw: 'both',
  bursts: 'picks',
  screenshots: true,
  hidden: false,
};

/** Why an asset was left out. */
export type ExclusionReason = 'screenshot' | 'hidden' | 'burst';

export type Excluded<T> = { asset: T; reason: ExclusionReason };

export type Selection<T> = {
  send: T[];
  excluded: Excluded<T>[];
  /** How many were left out, by reason, for a line of text on screen. */
  counts: Record<ExclusionReason, number>;
};

/**
 * Splits a library listing into what will be sent and what will not.
 *
 * Nothing is removed silently. A person who chose "skip screenshots" and then
 * counted their photos has to be able to see where the difference went.
 */
export function selectAssets<T extends { flags: AssetFlags }>(
  assets: T[],
  options: SendOptions,
): Selection<T> {
  const send: T[] = [];
  const excluded: Excluded<T>[] = [];
  const counts: Record<ExclusionReason, number> = { screenshot: 0, hidden: 0, burst: 0 };

  for (const asset of assets) {
    const reason = exclusionFor(asset.flags, options);
    if (reason) {
      excluded.push({ asset, reason });
      counts[reason] += 1;
    } else {
      send.push(asset);
    }
  }

  return { send, excluded, counts };
}

function exclusionFor(flags: AssetFlags, options: SendOptions): ExclusionReason | null {
  // Hidden is checked first: an asset that is both hidden and a screenshot was
  // hidden deliberately, and that is the more specific decision.
  if (flags.isHidden && !options.hidden) return 'hidden';
  if (flags.isScreenshot && !options.screenshots) return 'screenshot';

  if (flags.burstId && options.bursts === 'picks') {
    // A burst is forty near-identical frames. Sending all of them by default
    // would multiply a transfer by forty for a photo the user took once.
    if (!flags.representsBurst && !flags.userPickedFromBurst) return 'burst';
  }
  return null;
}

/** One resource chosen for sending. */
export type Chosen = {
  type: ResourceType;
  filename: string;
  size: number;
  /**
   * `primary` for the still (or the lone video), `secondary` for everything
   * that hangs off it. The receiver allocates one basename per pair and gives
   * each member its own extension (docs/PROTOCOL.md §5.1).
   */
  role: 'primary' | 'secondary';
  /** Whether the file behind this is a video, for the tus `kind` metadata. */
  kind: 'photo' | 'video';
};

/**
 * Decides which of an asset's resources to send.
 *
 * The one rule that overrides every option: **never return nothing.** An
 * option that would leave an asset with no resource at all is an option that
 * silently drops a photo, so each branch falls back to whatever is actually
 * present.
 */
export function chooseResources(
  resources: Resource[],
  flags: AssetFlags,
  options: SendOptions,
  kind: 'photo' | 'video',
): Chosen[] {
  const byType = new Map<ResourceType, Resource>();
  for (const resource of resources) {
    // First wins: iOS lists resources in a stable order and a duplicate type
    // is not something to guess about.
    if (!byType.has(resource.type)) byType.set(resource.type, resource);
  }

  const chosen: Resource[] = [];
  const add = (resource: Resource | undefined): void => {
    if (resource && !chosen.includes(resource)) chosen.push(resource);
  };

  if (kind === 'video') {
    const original = byType.get('video');
    const rendered = byType.get('fullSizeVideo');
    addVersions(add, original, rendered, flags, options);
  } else {
    const original = byType.get('photo');
    const rendered = byType.get('fullSizePhoto');
    const alternate = byType.get('alternatePhoto');

    if (original && isRaw(original)) {
      // A raw negative and the JPEG beside it are two different photographs
      // as far as the user is concerned: one is the thing you edit, one is
      // the thing you send to people.
      switch (options.raw) {
        case 'raw':
          add(original);
          break;
        case 'jpeg':
          // Falls back to the negative when there is no JPEG. Sending nothing
          // would lose the shot entirely.
          add(alternate ?? rendered ?? original);
          break;
        default:
          add(original);
          add(alternate ?? rendered);
      }
    } else {
      addVersions(add, original, rendered, flags, options);
      // A JPEG alongside a non-raw still is unusual, but if it is there it is
      // a separate capture of the same moment and belongs in the backup.
      if (options.raw === 'both') add(alternate);
    }
  }

  if (options.livePhotoVideo) {
    const paired = byType.get('pairedVideo');
    const pairedRendered = byType.get('fullSizePairedVideo');
    // The rendered motion matches the rendered still, so an edited Live Photo
    // that sends its edited half must send the matching video or it will play
    // as two different photos.
    add(options.edits === 'original' ? (paired ?? pairedRendered) : (pairedRendered ?? paired));
    if (options.edits === 'both') add(paired ?? pairedRendered);
  }

  if (chosen.length === 0) {
    // Every option said no, or the asset has resources of types this version
    // does not know. Send the first thing there is rather than nothing.
    const fallback = resources.find((r) => !isSidecarData(r.type));
    if (fallback) chosen.push(fallback);
  }

  return chosen.map((resource, index) => ({
    type: resource.type,
    filename: resource.filename,
    size: resource.size,
    role: index === 0 ? 'primary' : 'secondary',
    kind: isVideoResource(resource.type) ? 'video' : 'photo',
  }));
}

/**
 * The photographic resources of an asset that `chooseResources` did not pick,
 * or `undefined` when that cannot be established.
 *
 * The counterpart question to `chooseResources`, and it exists for one caller:
 * delete-after-transfer. Sending the rendered version of an edited photo and
 * then deleting the asset destroys the untouched capture, which was never on
 * the receiver and cannot be got back. Sending the JPEG of a ProRAW shot and
 * deleting the asset destroys the negative.
 *
 * So a phone may only delete an asset it sent *all* of, and this is how it
 * knows. An edit recipe does not count -- it is not a photograph, and it is
 * deliberately never sent (docs/PROTOCOL.md §5.1).
 *
 * An empty resource list answers `undefined` rather than "nothing withheld".
 * `PHAssetResource` returns one for an asset whose resources are not
 * available, without failing, and reading that as "fully accounted for" would
 * let the asset be deleted after sending whatever `getUri` happened to
 * return. Undefined blocks the deletion (src/core/deletion.ts), which is the
 * only safe reading of "we could not find out".
 */
export function withheldResources(
  resources: Resource[],
  chosen: Chosen[],
): ResourceType[] | undefined {
  if (resources.length === 0) return undefined;

  // Counted rather than set-tested. `chooseResources` keeps only the first
  // resource of each type, so an asset listing two of a type sends one and
  // leaves one behind -- and a membership test on the type would call that
  // "nothing withheld" and let the asset be deleted.
  const sentPerType = new Map<ResourceType, number>();
  for (const c of chosen) sentPerType.set(c.type, (sentPerType.get(c.type) ?? 0) + 1);

  const seenPerType = new Map<ResourceType, number>();
  const withheld: ResourceType[] = [];

  for (const resource of resources) {
    if (isSidecarData(resource.type)) continue;

    const index = seenPerType.get(resource.type) ?? 0;
    seenPerType.set(resource.type, index + 1);

    // The nth resource of a type is covered only if at least n of that type
    // were actually sent.
    if (index < (sentPerType.get(resource.type) ?? 0)) continue;
    if (withheld.includes(resource.type)) continue;
    withheld.push(resource.type);
  }
  return withheld;
}

// addVersions applies the edited/original choice to one pair of resources.
function addVersions(
  add: (r: Resource | undefined) => void,
  original: Resource | undefined,
  rendered: Resource | undefined,
  flags: AssetFlags,
  options: SendOptions,
): void {
  // With no edits there is only one version, whatever the setting says.
  if (!flags.hasAdjustments || !rendered) {
    add(original ?? rendered);
    return;
  }

  switch (options.edits) {
    case 'original':
      add(original ?? rendered);
      break;
    case 'both':
      // The rendered version first: it is the one the user recognises, and
      // the primary is what the pair's basename is taken from.
      add(rendered);
      add(original);
      break;
    default:
      add(rendered ?? original);
  }
}

/** UTIs iOS uses for a raw negative. */
const RAW_UTIS = new Set(['com.adobe.raw-image', 'public.camera-raw-image', 'com.adobe.dng']);

function isRaw(resource: Resource): boolean {
  if (RAW_UTIS.has(resource.uti)) return true;
  // Apple ProRAW arrives as a DNG, and third-party cameras use their own
  // UTIs; the extension is the reliable part in both cases.
  return /\.(dng|arw|cr2|cr3|nef|orf|raf|rw2|pef|srw)$/i.test(resource.filename);
}

function isVideoResource(type: ResourceType): boolean {
  return (
    type === 'video' ||
    type === 'fullSizeVideo' ||
    type === 'pairedVideo' ||
    type === 'fullSizePairedVideo' ||
    type === 'adjustmentBaseVideo' ||
    type === 'adjustmentBasePairedVideo'
  );
}

/**
 * Resources that are not a copy of the photograph.
 *
 * `adjustmentData` is the edit recipe -- a few kilobytes that mean nothing
 * without Photos to apply them. Sending it as if it were a photo would put a
 * file called `FullSizeRender.plist` in somebody's holiday folder.
 */
function isSidecarData(type: ResourceType): boolean {
  // photoProxy is a placeholder for an asset iOS is still writing, and is
  // never the photograph either.
  return type === 'adjustmentData' || type === 'photoProxy';
}

/**
 * A one-line summary of what was left out, or "" when nothing was.
 *
 * On screen rather than in a log: a transfer that sent 2,300 of 2,431 photos
 * has to say where the other 131 went, in the same place it says the 2,300.
 */
export function describeExclusions(counts: Record<ExclusionReason, number>): string {
  const parts: string[] = [];
  if (counts.screenshot > 0) parts.push(`${counts.screenshot} screenshots`);
  if (counts.burst > 0) parts.push(`${counts.burst} extra burst frames`);
  if (counts.hidden > 0) parts.push(`${counts.hidden} hidden`);
  if (parts.length === 0) return '';
  return `Skipping ${joinWords(parts)}.`;
}

function joinWords(parts: string[]): string {
  if (parts.length === 1) return parts[0]!;
  return `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]!}`;
}
