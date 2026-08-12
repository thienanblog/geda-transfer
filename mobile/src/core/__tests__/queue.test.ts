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

import { CancelledError, PausedError, WorkQueue, isAbort } from '../queue';

/** A job that finishes when the test says so. */
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe('WorkQueue', () => {
  it('runs no more than the limit at once', async () => {
    // The limit is the whole point: it is what keeps six to eight streams on
    // one HTTP/2 connection instead of two hundred.
    const gates = Array.from({ length: 10 }, deferred);
    let peak = 0;
    let running = 0;

    const queue = new WorkQueue<number>(async (item) => {
      running += 1;
      peak = Math.max(peak, running);
      await gates[item]!.promise;
      running -= 1;
    }, { limit: 3 });

    queue.add([0, 1, 2, 3, 4, 5, 6, 7, 8, 9]);
    await tick();

    expect(queue.running).toBe(3);
    expect(peak).toBe(3);

    gates.forEach((gate) => gate.resolve());
    await queue.whenIdle();
    expect(peak).toBe(3);
  });

  it('aborts in-flight work when paused and retries it on resume', async () => {
    // Pause has to mean now. A 4 GB video would otherwise keep going for
    // minutes after the tap, and the upload resumes from its offset anyway.
    const attempts: number[] = [];
    let aborted = false;

    const queue = new WorkQueue<string>(async (item, { signal }) => {
      attempts.push(attempts.length);
      await new Promise<void>((resolve, reject) => {
        signal.addEventListener('abort', () => {
          aborted = true;
          reject(signal.reason);
        });
        if (item === 'fast') resolve();
      });
    }, { limit: 1 });

    queue.add(['slow']);
    await tick();

    queue.pause();
    await tick();

    expect(aborted).toBe(true);
    expect(queue.isPaused).toBe(true);
    expect(queue.running).toBe(0);
    // Requeued rather than dropped, and at the front: the transfer carries on
    // with the file the user was watching.
    expect(queue.queued).toBe(1);

    queue.cancel();
    await queue.whenIdle();
  });

  it('discards queued work on cancel', async () => {
    const queue = new WorkQueue<number>(async (_item, { signal }) => {
      await new Promise<void>((_resolve, reject) => {
        signal.addEventListener('abort', () => reject(signal.reason));
      });
    }, { limit: 2 });

    queue.add([1, 2, 3, 4, 5]);
    await tick();
    expect(queue.running).toBe(2);

    queue.cancel();
    await queue.whenIdle();

    expect(queue.running).toBe(0);
    expect(queue.queued).toBe(0);
  });

  it('keeps going after a failure', async () => {
    // One unreadable photo out of nine hundred must not end the transfer.
    const seen: number[] = [];
    const queue = new WorkQueue<number>(async (item) => {
      seen.push(item);
      if (item === 2) throw new Error('this one is broken');
    }, { limit: 1 });

    queue.add([1, 2, 3]);
    await queue.whenIdle();

    expect(seen).toEqual([1, 2, 3]);
  });

  it('reports idle once, after everything has drained', async () => {
    let idle = 0;
    const queue = new WorkQueue<number>(async () => {}, {
      limit: 2,
      onIdle: () => {
        idle += 1;
      },
    });

    queue.add([1, 2, 3]);
    await queue.whenIdle();

    expect(idle).toBeGreaterThan(0);
    expect(queue.running).toBe(0);
  });
});

describe('isAbort', () => {
  it('separates a pause or a cancel from a real failure', () => {
    // The worker uses this to decide whether to record an error on the item.
    // A paused upload is not a failed one, and showing it in red would be a
    // lie about what happened.
    expect(isAbort(new PausedError())).toBe(true);
    expect(isAbort(new CancelledError())).toBe(true);
    expect(isAbort(new Error('the network went away'))).toBe(false);
    expect(isAbort(undefined)).toBe(false);
  });
});
