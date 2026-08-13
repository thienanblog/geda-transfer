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

import { describe, expect, it } from 'vitest';

import {
  DEFAULT_SEND_OPTIONS,
  NO_FLAGS,
  chooseResources,
  describeExclusions,
  selectAssets,
  type AssetFlags,
  type Resource,
  type ResourceType,
  type SendOptions,
} from '../selection';

function resource(type: ResourceType, filename: string, uti = 'public.heic'): Resource {
  return { type, filename, uti, size: 1000 };
}

function flags(over: Partial<AssetFlags> = {}): AssetFlags {
  return { ...NO_FLAGS, ...over };
}

function options(over: Partial<SendOptions> = {}): SendOptions {
  return { ...DEFAULT_SEND_OPTIONS, ...over };
}

const HEIC = resource('photo', 'IMG_0042.HEIC');
const MOTION = resource('pairedVideo', 'IMG_0042.MOV', 'com.apple.quicktime-movie');
const DNG = resource('photo', 'IMG_0042.DNG', 'com.adobe.raw-image');
const JPEG = resource('alternatePhoto', 'IMG_0042.JPG', 'public.jpeg');
const RENDER = resource('fullSizePhoto', 'FullSizeRender.HEIC');

describe('the defaults', () => {
  it('sends more rather than less, except for what was hidden on purpose', () => {
    // A backup is only worth having if the thing you did not think about is
    // in it. Hidden is the one exception, and it is a privacy decision the
    // user already made.
    expect(DEFAULT_SEND_OPTIONS.livePhotoVideo).toBe(true);
    expect(DEFAULT_SEND_OPTIONS.raw).toBe('both');
    expect(DEFAULT_SEND_OPTIONS.screenshots).toBe(true);
    expect(DEFAULT_SEND_OPTIONS.hidden).toBe(false);
  });
});

describe('selectAssets', () => {
  const asset = (over: Partial<AssetFlags>) => ({ flags: flags(over) });

  it('sends everything by default except hidden assets', () => {
    const result = selectAssets(
      [
        asset({}),
        asset({ isScreenshot: true }),
        asset({ isHidden: true }),
        asset({ burstId: 'b1', representsBurst: true }),
      ],
      DEFAULT_SEND_OPTIONS,
    );

    expect(result.send).toHaveLength(3);
    expect(result.counts.hidden).toBe(1);
    expect(result.counts.screenshot).toBe(0);
  });

  it('keeps only the picked frames of a burst', () => {
    // Forty near-identical frames for a photo the user took once. Sending all
    // of them by default would multiply the transfer by forty.
    const result = selectAssets(
      [
        asset({ burstId: 'b1', representsBurst: true }),
        asset({ burstId: 'b1', userPickedFromBurst: true }),
        asset({ burstId: 'b1' }),
        asset({ burstId: 'b1' }),
      ],
      options({ bursts: 'picks' }),
    );

    expect(result.send).toHaveLength(2);
    expect(result.counts.burst).toBe(2);
  });

  it('sends every burst frame when asked', () => {
    const frames = [
      asset({ burstId: 'b1', representsBurst: true }),
      asset({ burstId: 'b1' }),
      asset({ burstId: 'b1' }),
    ];
    expect(selectAssets(frames, options({ bursts: 'all' })).send).toHaveLength(3);
  });

  it('skips screenshots when asked', () => {
    const result = selectAssets(
      [asset({}), asset({ isScreenshot: true })],
      options({ screenshots: false }),
    );
    expect(result.send).toHaveLength(1);
    expect(result.counts.screenshot).toBe(1);
  });

  it('sends hidden assets only when asked', () => {
    const result = selectAssets([asset({ isHidden: true })], options({ hidden: true }));
    expect(result.send).toHaveLength(1);
  });

  // A hidden screenshot was hidden deliberately; that is the more specific
  // decision and the one the count should name.
  it('reports a hidden screenshot as hidden', () => {
    const result = selectAssets(
      [asset({ isHidden: true, isScreenshot: true })],
      options({ screenshots: false }),
    );
    expect(result.excluded[0]!.reason).toBe('hidden');
  });

  it('describes what it left out', () => {
    expect(describeExclusions({ screenshot: 3, burst: 12, hidden: 1 })).toBe(
      'Skipping 3 screenshots, 12 extra burst frames and 1 hidden.',
    );
    expect(describeExclusions({ screenshot: 0, burst: 0, hidden: 0 })).toBe('');
  });
});

describe('chooseResources', () => {
  it('sends a Live Photo as a pair', () => {
    const chosen = chooseResources([HEIC, MOTION], flags(), DEFAULT_SEND_OPTIONS, 'photo');

    expect(chosen.map((c) => c.type)).toEqual(['photo', 'pairedVideo']);
    expect(chosen[0]!.role).toBe('primary');
    expect(chosen[1]!.role).toBe('secondary');
    // The kind is what decides the still from the motion on the receiver.
    expect(chosen[1]!.kind).toBe('video');
  });

  it('sends only the still when the motion is turned off', () => {
    const chosen = chooseResources(
      [HEIC, MOTION],
      flags(),
      options({ livePhotoVideo: false }),
      'photo',
    );
    expect(chosen.map((c) => c.type)).toEqual(['photo']);
  });

  // Half of the P8 gate. A ProRAW shot has to keep its negative, and the
  // negative has to be the primary -- it is the file the user cares about.
  it('keeps the DNG of a ProRAW shot', () => {
    for (const raw of ['both', 'raw'] as const) {
      const chosen = chooseResources([DNG, JPEG], flags(), options({ raw }), 'photo');
      expect(chosen[0]!.type).toBe('photo');
      expect(chosen[0]!.filename).toBe('IMG_0042.DNG');
      expect(chosen[0]!.role).toBe('primary');
    }
  });

  it('sends the negative and the JPEG as a pair by default', () => {
    const chosen = chooseResources([DNG, JPEG], flags(), DEFAULT_SEND_OPTIONS, 'photo');
    expect(chosen.map((c) => c.filename)).toEqual(['IMG_0042.DNG', 'IMG_0042.JPG']);
  });

  it('sends only the JPEG when asked', () => {
    const chosen = chooseResources([DNG, JPEG], flags(), options({ raw: 'jpeg' }), 'photo');
    expect(chosen.map((c) => c.filename)).toEqual(['IMG_0042.JPG']);
  });

  // "Only the JPEG" on a shot that has no JPEG must not mean "nothing".
  it('falls back to the negative when there is no JPEG to send instead', () => {
    const chosen = chooseResources([DNG], flags(), options({ raw: 'jpeg' }), 'photo');
    expect(chosen.map((c) => c.filename)).toEqual(['IMG_0042.DNG']);
  });

  it('sends the edited version of an edited photo by default', () => {
    const chosen = chooseResources(
      [HEIC, RENDER, resource('adjustmentData', 'FullSizeRender.plist')],
      flags({ hasAdjustments: true }),
      DEFAULT_SEND_OPTIONS,
      'photo',
    );
    expect(chosen.map((c) => c.type)).toEqual(['fullSizePhoto']);
  });

  it('sends the untouched capture when asked', () => {
    const chosen = chooseResources(
      [HEIC, RENDER],
      flags({ hasAdjustments: true }),
      options({ edits: 'original' }),
      'photo',
    );
    expect(chosen.map((c) => c.type)).toEqual(['photo']);
  });

  it('sends both versions as a pair when asked', () => {
    const chosen = chooseResources(
      [HEIC, RENDER],
      flags({ hasAdjustments: true }),
      options({ edits: 'both' }),
      'photo',
    );
    // The render first: it is the one the user recognises, and the primary is
    // where the pair's basename comes from.
    expect(chosen.map((c) => c.type)).toEqual(['fullSizePhoto', 'photo']);
    expect(chosen[0]!.role).toBe('primary');
  });

  // An unedited photo has one version whatever the setting says, and asking
  // for "the original" must not mean a second copy of the same file.
  it('sends one file for an unedited photo under every edit setting', () => {
    for (const edits of ['edited', 'original', 'both'] as const) {
      const chosen = chooseResources([HEIC], flags(), options({ edits }), 'photo');
      expect(chosen.map((c) => c.type)).toEqual(['photo']);
    }
  });

  it('matches the motion to the still it was sent with', () => {
    const resources = [
      HEIC,
      RENDER,
      MOTION,
      resource('fullSizePairedVideo', 'FullSizeRender.MOV', 'com.apple.quicktime-movie'),
    ];

    const edited = chooseResources(
      resources,
      flags({ hasAdjustments: true }),
      DEFAULT_SEND_OPTIONS,
      'photo',
    );
    expect(edited.map((c) => c.type)).toEqual(['fullSizePhoto', 'fullSizePairedVideo']);

    // Sending the edited still with the unedited motion would play as two
    // different photographs.
    const original = chooseResources(
      resources,
      flags({ hasAdjustments: true }),
      options({ edits: 'original' }),
      'photo',
    );
    expect(original.map((c) => c.type)).toEqual(['photo', 'pairedVideo']);
  });

  it('sends a plain video as one file', () => {
    const chosen = chooseResources(
      [resource('video', 'IMG_0043.MOV', 'com.apple.quicktime-movie')],
      flags(),
      DEFAULT_SEND_OPTIONS,
      'video',
    );
    expect(chosen.map((c) => c.type)).toEqual(['video']);
    expect(chosen[0]!.kind).toBe('video');
  });

  // The edit recipe is a few kilobytes that mean nothing without Photos.
  // Sending it as if it were a photo would put a .plist in a holiday folder.
  it('never sends the adjustment data', () => {
    const chosen = chooseResources(
      [HEIC, resource('adjustmentData', 'FullSizeRender.plist')],
      flags({ hasAdjustments: true }),
      options({ edits: 'both' }),
      'photo',
    );
    expect(chosen.map((c) => c.type)).not.toContain('adjustmentData');
  });

  // The rule that overrides every option: an asset must never resolve to
  // nothing, or a photo disappears from the backup without a word.
  it('never returns nothing when there is something to send', () => {
    const odd = resource('unknown', 'MYSTERY.BIN', 'public.data');
    const chosen = chooseResources([odd], flags(), DEFAULT_SEND_OPTIONS, 'photo');
    expect(chosen).toHaveLength(1);
    expect(chosen[0]!.filename).toBe('MYSTERY.BIN');
  });

  it('returns nothing for an asset with no resources at all', () => {
    expect(chooseResources([], flags(), DEFAULT_SEND_OPTIONS, 'photo')).toHaveLength(0);
  });
});
