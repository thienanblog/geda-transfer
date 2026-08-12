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
 * A bounded-concurrency work queue with pause and cancel.
 *
 * Concurrency is the whole point: six to eight uploads in flight over one
 * HTTP/2 connection is what saturates a Wi-Fi 6 link, while one at a time
 * spends most of the transfer waiting for round trips (AGENTS.md §3.2, §5).
 *
 * Pausing stops *starting* work and asks what is in flight to stop. It does
 * not wait for a 4K video to finish first, because a person who taps pause
 * means now -- and an interrupted upload resumes from its offset, so nothing
 * is lost by stopping mid-file.
 */

export type WorkerContext = {
  /** Aborted when the item is cancelled, or the whole queue is paused or cancelled. */
  signal: AbortSignal;
};

export type QueueOptions = {
  /** How many items run at once. */
  limit: number;
  onIdle?: () => void;
};

type Job<T> = {
  item: T;
  attempt: number;
};

export class WorkQueue<T> {
  private readonly limit: number;
  private pending: Job<T>[] = [];
  private readonly inFlight = new Map<T, AbortController>();
  private paused = false;
  private stopped = false;
  private draining = false;
  private idleWaiters: (() => void)[] = [];

  constructor(
    private readonly worker: (item: T, context: WorkerContext) => Promise<void>,
    private readonly options: QueueOptions,
  ) {
    this.limit = Math.max(1, options.limit);
  }

  get running(): number {
    return this.inFlight.size;
  }

  get queued(): number {
    return this.pending.length;
  }

  get isPaused(): boolean {
    return this.paused;
  }

  add(items: T[]): void {
    this.pending.push(...items.map((item) => ({ item, attempt: 0 })));
    this.pump();
  }

  pause(): void {
    if (this.paused) return;
    this.paused = true;
    // In-flight items go back to the front of the queue: they resume from
    // whatever offset the receiver already holds, so restarting them is cheap.
    for (const controller of this.inFlight.values()) {
      controller.abort(new PausedError());
    }
  }

  resume(): void {
    if (!this.paused) return;
    this.paused = false;
    this.pump();
  }

  /** Stops everything and discards what has not started. */
  cancel(): void {
    this.stopped = true;
    this.pending = [];
    for (const controller of this.inFlight.values()) {
      controller.abort(new CancelledError());
    }
  }

  /** Resolves when nothing is queued and nothing is in flight. */
  async whenIdle(): Promise<void> {
    if (this.isIdle()) return;
    await new Promise<void>((resolve) => this.idleWaiters.push(resolve));
  }

  /**
   * Idle means finished, not merely quiet.
   *
   * A paused queue with work left in it is not idle: whoever is waiting for
   * the transfer to end is still waiting, and resolving here would tear down
   * the progress listener while the user is looking at a Resume button.
   */
  private isIdle(): boolean {
    return this.inFlight.size === 0 && (this.pending.length === 0 || this.stopped);
  }

  private pump(): void {
    if (this.draining) return;
    this.draining = true;

    try {
      while (!this.paused && !this.stopped && this.inFlight.size < this.limit) {
        const job = this.pending.shift();
        if (!job) break;
        void this.run(job);
      }
    } finally {
      this.draining = false;
    }

    if (this.isIdle()) {
      this.options.onIdle?.();
      const waiters = this.idleWaiters;
      this.idleWaiters = [];
      for (const resolve of waiters) resolve();
    }
  }

  private async run(job: Job<T>): Promise<void> {
    const controller = new AbortController();
    this.inFlight.set(job.item, controller);

    try {
      await this.worker(job.item, { signal: controller.signal });
    } catch (error) {
      if (error instanceof PausedError && !this.stopped) {
        // Requeued at the front: a paused transfer should carry on with the
        // file the user was watching, not start something else.
        this.pending.unshift({ item: job.item, attempt: job.attempt + 1 });
      }
      // Anything else is the worker's business. It records the failure on the
      // item; one bad file must not stop the other nine hundred.
    } finally {
      this.inFlight.delete(job.item);
      this.pump();
    }
  }
}

export class PausedError extends Error {
  constructor() {
    super('paused');
    this.name = 'PausedError';
  }
}

export class CancelledError extends Error {
  constructor() {
    super('cancelled');
    this.name = 'CancelledError';
  }
}

export function isAbort(error: unknown): boolean {
  return error instanceof PausedError || error instanceof CancelledError;
}
