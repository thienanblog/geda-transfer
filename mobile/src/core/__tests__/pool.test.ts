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

import { mapPool } from '../pool';

describe('mapPool', () => {
  it('never runs more than the limit at once', async () => {
    let running = 0;
    let peak = 0;

    await mapPool(
      Array.from({ length: 20 }, (_, index) => index),
      async (value) => {
        running += 1;
        peak = Math.max(peak, running);
        await Promise.resolve();
        running -= 1;
        return value;
      },
      { concurrency: 4 },
    );

    expect(peak).toBe(4);
  });

  it('carries on past a failure', async () => {
    // One photo stuck in iCloud must not stop the other four hundred.
    const failures: unknown[] = [];
    const results = await mapPool(
      ['a', 'bad', 'b'],
      async (value) => {
        if (value === 'bad') throw new Error('is in iCloud');
        return value.toUpperCase();
      },
      { concurrency: 1, onError: (error) => failures.push(error) },
    );

    expect(results).toEqual(['A', 'B']);
    expect(failures).toHaveLength(1);
  });

  it('stops when asked to', async () => {
    let done = 0;
    await mapPool(
      Array.from({ length: 100 }, (_, index) => index),
      async (value) => {
        done += 1;
        return value;
      },
      { concurrency: 1, shouldContinue: () => done < 3 },
    );

    expect(done).toBe(3);
  });

  it('does nothing with nothing', async () => {
    expect(await mapPool([], async (value) => value)).toEqual([]);
  });
});
