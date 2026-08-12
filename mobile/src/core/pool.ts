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

/**
 * Running an async function over a list, a few at a time.
 *
 * Both transfer paths need this for the same reason: getting a file path out
 * of a `PHAsset` is the slowest step in a transfer, ahead of the network
 * (AGENTS.md §5), it is I/O rather than CPU, and about six at once is where an
 * iPhone stops going faster. One at a time wastes most of the wait.
 *
 * Failures are per item. One photo stuck in iCloud must not stop the other
 * four hundred, so the error is reported and the pool carries on.
 */

export type PoolOptions<T> = {
  concurrency?: number;
  /** Checked between items, so a cancelled transfer stops promptly. */
  shouldContinue?: () => boolean;
  onError?: (error: unknown, item: T) => void;
};

export const DEFAULT_POOL_CONCURRENCY = 6;

export async function mapPool<T, R>(
  items: readonly T[],
  worker: (item: T) => Promise<R>,
  options: PoolOptions<T> = {},
): Promise<R[]> {
  const limit = Math.max(1, options.concurrency ?? DEFAULT_POOL_CONCURRENCY);
  const results: R[] = [];
  let index = 0;

  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (index < items.length && (options.shouldContinue?.() ?? true)) {
      const item = items[index]!;
      index += 1;
      try {
        results.push(await worker(item));
      } catch (error) {
        options.onError?.(error, item);
      }
    }
  });

  await Promise.all(workers);
  return results;
}
