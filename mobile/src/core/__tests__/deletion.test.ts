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
  describeKept,
  planDeletions,
  worthAsking,
  type Confirmation,
  type DeletionCandidate,
} from '../deletion';
import {
  DEFAULT_SEND_OPTIONS,
  NO_FLAGS,
  chooseResources,
  withheldResources,
  type Resource,
  type ResourceType,
} from '../selection';

const DIGEST = 'a'.repeat(64);

function candidate(over: Partial<DeletionCandidate> = {}): DeletionCandidate {
  return {
    assetId: 'asset-1',
    key: 'asset-1:1000',
    storedPath: '2026/07/IMG_1.HEIC',
    size: 1000,
    sha256: DIGEST,
    withheld: [],
    filename: 'IMG_1.HEIC',
    ...over,
  };
}

function confirmed(key: string): Confirmation {
  return { key, confirmed: true };
}

describe('planDeletions', () => {
  it('deletes an asset whose every file the receiver vouched for', () => {
    const plan = planDeletions([candidate()], [confirmed('asset-1:1000')]);

    expect(plan.deletable).toEqual(['asset-1']);
    expect(plan.kept).toEqual([]);
  });

  // The failure this whole module exists to prevent: a confirmation that
  // never arrived being read as a yes.
  it('keeps anything the receiver did not answer about', () => {
    const plan = planDeletions([candidate()], []);

    expect(plan.deletable).toEqual([]);
    expect(plan.kept).toEqual([{ assetId: 'asset-1', filename: 'IMG_1.HEIC', reason: 'unanswered' }]);
  });

  it('keeps anything the receiver refused, and says why', () => {
    const plan = planDeletions(
      [candidate()],
      [{ key: 'asset-1:1000', confirmed: false, reason: 'content_mismatch' }],
    );

    expect(plan.deletable).toEqual([]);
    expect(plan.kept[0]).toMatchObject({ reason: 'unconfirmed', detail: 'content_mismatch' });
  });

  // A Live Photo is one asset and two files. Deleting it on the strength of
  // the still leaves the motion nowhere.
  it('keeps a pair when only one half was confirmed', () => {
    const still = candidate({ key: 'asset-1:1000' });
    const motion = candidate({ key: 'asset-1:pairedVideo', filename: 'IMG_1.MOV' });

    const plan = planDeletions([still, motion], [confirmed('asset-1:1000')]);

    expect(plan.deletable).toEqual([]);
    expect(plan.kept[0].reason).toBe('unanswered');
  });

  it('deletes a pair once both halves are confirmed', () => {
    const still = candidate({ key: 'asset-1:1000' });
    const motion = candidate({ key: 'asset-1:pairedVideo', filename: 'IMG_1.MOV' });

    const plan = planDeletions(
      [still, motion],
      [confirmed('asset-1:1000'), confirmed('asset-1:pairedVideo')],
    );

    expect(plan.deletable).toEqual(['asset-1']);
  });

  // Sending the JPEG of a ProRAW shot and then deleting the asset destroys a
  // negative that was never on the receiver.
  it('keeps an asset whose other resources were never sent', () => {
    const plan = planDeletions(
      [candidate({ withheld: ['photo'] })],
      [confirmed('asset-1:1000')],
    );

    expect(plan.deletable).toEqual([]);
    expect(plan.kept[0]).toMatchObject({ reason: 'withheld', detail: 'photo' });
  });

  // An unknown is not a no. A candidate that never established what else the
  // asset holds is treated exactly as one that knows something is missing.
  it('keeps an asset whose resources were never established', () => {
    const plan = planDeletions(
      [candidate({ withheld: undefined })],
      [confirmed('asset-1:1000')],
    );

    expect(plan.deletable).toEqual([]);
    expect(plan.kept[0].reason).toBe('withheld');
  });

  it.each([
    ['no stored path', { storedPath: '' }],
    ['no digest', { sha256: '' }],
    ['a digest that is not one', { sha256: 'not a digest' }],
    ['a truncated digest', { sha256: 'ab12' }],
  ])('keeps a candidate with %s however the receiver answered', (_name, over) => {
    const plan = planDeletions([candidate(over)], [confirmed('asset-1:1000')]);

    expect(plan.deletable).toEqual([]);
    expect(plan.kept[0].reason).toBe('unconfirmed');
  });

  // A receiver that answers twice has already said the thing that matters.
  it('does not let a second, cheerful answer overwrite a refusal', () => {
    const plan = planDeletions(
      [candidate()],
      [
        { key: 'asset-1:1000', confirmed: false, reason: 'missing' },
        confirmed('asset-1:1000'),
      ],
    );

    expect(plan.deletable).toEqual([]);
  });

  it('ignores answers about files it did not ask about', () => {
    const plan = planDeletions([candidate()], [confirmed('someone-elses-key')]);

    expect(plan.deletable).toEqual([]);
  });

  it('separates the assets that may go from the ones that may not', () => {
    const plan = planDeletions(
      [
        candidate({ assetId: 'good', key: 'good:1' }),
        candidate({ assetId: 'bad', key: 'bad:1', filename: 'IMG_2.HEIC' }),
      ],
      [confirmed('good:1'), { key: 'bad:1', confirmed: false, reason: 'missing' }],
    );

    expect(plan.deletable).toEqual(['good']);
    expect(plan.kept).toHaveLength(1);
    expect(plan.kept[0].assetId).toBe('bad');
  });

  it('has nothing to do with an empty library', () => {
    expect(planDeletions([], [])).toEqual({ deletable: [], kept: [] });
  });
});

describe('withheldResources', () => {
  function resource(type: ResourceType, filename: string, uti = 'public.heic'): Resource {
    return { type, filename, uti, size: 1000 };
  }

  // An empty listing is not evidence that nothing was left behind:
  // PHAssetResource returns one for an asset whose resources are unavailable,
  // without failing. Reading it as "fully accounted for" would delete the
  // asset after sending whatever the library's default representation was.
  it('cannot establish anything from an empty resource list', () => {
    expect(withheldResources([], [])).toBeUndefined();
  });

  // `chooseResources` keeps only the first resource of each type, so a second
  // one of that type is never sent -- and must not be masked by its twin.
  it('reports a duplicate-typed resource that was never sent', () => {
    const resources = [
      resource('alternatePhoto', 'IMG_1.JPG', 'public.jpeg'),
      resource('alternatePhoto', 'IMG_1_alt.JPG', 'public.jpeg'),
      resource('photo', 'IMG_1.DNG', 'com.adobe.dng'),
    ];
    const chosen = chooseResources(resources, NO_FLAGS, DEFAULT_SEND_OPTIONS, 'photo');

    expect(chosen.filter((c) => c.type === 'alternatePhoto')).toHaveLength(1);
    expect(withheldResources(resources, chosen)).toEqual(['alternatePhoto']);
  });

  it('reports nothing when everything photographic was sent', () => {
    const resources = [resource('photo', 'IMG_1.HEIC'), resource('pairedVideo', 'IMG_1.MOV')];
    const chosen = chooseResources(resources, NO_FLAGS, DEFAULT_SEND_OPTIONS, 'photo');

    expect(withheldResources(resources, chosen)).toEqual([]);
  });

  // The edit recipe is not a photograph and is deliberately never sent, so it
  // must not be what stops a library from ever being deletable.
  it('does not count the edit recipe as something left behind', () => {
    const resources = [
      resource('photo', 'IMG_1.HEIC'),
      resource('adjustmentData', 'FullSizeRender.plist', 'com.apple.photos.adjustment-data'),
    ];
    const chosen = chooseResources(resources, NO_FLAGS, DEFAULT_SEND_OPTIONS, 'photo');

    expect(withheldResources(resources, chosen)).toEqual([]);
  });

  it('reports the negative when the user chose to send only the JPEG', () => {
    const resources = [
      resource('photo', 'IMG_1.DNG', 'com.adobe.dng'),
      resource('alternatePhoto', 'IMG_1.JPG', 'public.jpeg'),
    ];
    const chosen = chooseResources(
      resources,
      NO_FLAGS,
      { ...DEFAULT_SEND_OPTIONS, raw: 'jpeg' },
      'photo',
    );

    expect(withheldResources(resources, chosen)).toEqual(['photo']);
  });

  // The default for an edited photo sends the rendered version only, so the
  // untouched capture stays behind -- and the asset must not be deletable.
  it('reports the untouched capture of an edited photo under the default', () => {
    const resources = [
      resource('photo', 'IMG_1.HEIC'),
      resource('fullSizePhoto', 'FullSizeRender.HEIC'),
    ];
    const chosen = chooseResources(
      resources,
      { ...NO_FLAGS, hasAdjustments: true },
      DEFAULT_SEND_OPTIONS,
      'photo',
    );

    expect(withheldResources(resources, chosen)).toEqual(['photo']);
  });

  it('reports nothing for an edited photo sent as both versions', () => {
    const resources = [
      resource('photo', 'IMG_1.HEIC'),
      resource('fullSizePhoto', 'FullSizeRender.HEIC'),
    ];
    const chosen = chooseResources(
      resources,
      { ...NO_FLAGS, hasAdjustments: true },
      { ...DEFAULT_SEND_OPTIONS, edits: 'both' },
      'photo',
    );

    expect(withheldResources(resources, chosen)).toEqual([]);
  });

  it('reports the motion when Live Photo video is turned off', () => {
    const resources = [resource('photo', 'IMG_1.HEIC'), resource('pairedVideo', 'IMG_1.MOV')];
    const chosen = chooseResources(
      resources,
      NO_FLAGS,
      { ...DEFAULT_SEND_OPTIONS, livePhotoVideo: false },
      'photo',
    );

    expect(withheldResources(resources, chosen)).toEqual(['pairedVideo']);
  });
});

describe('worthAsking', () => {
  // The receiver reads the whole file to answer one of these, and rows that
  // can never be deleted are never cleared -- so asking about them would
  // re-read them on every transfer, for answers that get discarded.
  it('is true only for a candidate an answer could actually free', () => {
    expect(worthAsking(candidate())).toBe(true);
  });

  it.each([
    ['part of the asset was withheld', { withheld: ['photo'] as ResourceType[] }],
    ['the resources were never established', { withheld: undefined }],
    ['there is no stored path', { storedPath: '' }],
    ['there is no usable digest', { sha256: 'nope' }],
  ])('is false when %s', (_name, over) => {
    expect(worthAsking(candidate(over))).toBe(false);
  });

  // The filter and the verdict must agree: anything not worth asking about
  // has to be something `planDeletions` would keep anyway.
  it('never hides a candidate that a confirmation would have freed', () => {
    const cases = [
      candidate(),
      candidate({ withheld: ['photo'] }),
      candidate({ withheld: undefined }),
      candidate({ storedPath: '' }),
      candidate({ sha256: 'nope' }),
    ];

    for (const entry of cases) {
      const confirmedAnyway = planDeletions([entry], [confirmed(entry.key)]);
      expect(confirmedAnyway.deletable.length > 0).toBe(worthAsking(entry));
    }
  });
});

describe('describeKept', () => {
  it('says nothing when nothing was kept', () => {
    expect(describeKept([])).toBe('');
  });

  it('accounts for every reason', () => {
    const text = describeKept([
      { assetId: 'a', filename: 'a', reason: 'withheld' },
      { assetId: 'b', filename: 'b', reason: 'unconfirmed' },
      { assetId: 'c', filename: 'c', reason: 'unanswered' },
    ]);

    expect(text).toContain('3 items were kept');
    expect(text).toContain('only part of them was sent');
    expect(text).toContain('could not vouch for');
    expect(text).toContain('did not answer about');
  });
});
