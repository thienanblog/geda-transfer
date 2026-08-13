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

// The transfer engine.
//
// It orchestrates; it never touches a byte of the files. Reading an asset and
// writing it to the socket happens entirely inside URLSession, because a
// megabyte crossing the JavaScript bridge per photo would cost more than the
// network does (AGENTS.md §3.8).
//
// Two phases, both measured. Resolving a PHAsset to a file is the slowest step
// in the whole business -- ahead of the network -- so it is worth knowing how
// much of a transfer was the phone and how much was the link (AGENTS.md §5).

import GedaTransfer, {
  type UploadCreatedEvent,
  type UploadProgressEvent,
} from '../../modules/geda-transfer';
import { uploadMetadata, buildPlan } from '../core/plan';
import { mapPool } from '../core/pool';
import { DEFAULT_SEND_OPTIONS, type SendOptions } from '../core/selection';
import { WorkQueue, isAbort } from '../core/queue';
import { ThroughputMeter } from '../core/throughput';
import type { Asset, Receiver, TransferItem } from '../core/types';
import { recordSent, sentKeys } from '../data/ledger';
import {
  AssetUnavailableError,
  release,
  resolveAsset,
  type AssetSummary,
} from '../media/library';
import { connect } from './session';

/**
 * How many uploads run at once.
 *
 * Six to eight streams over one HTTP/2 connection saturate a Wi-Fi 6 link;
 * fewer leaves the link idle between round trips, more just makes the
 * receiver's disk seek (AGENTS.md §3.2). The receiver states its own limit at
 * pairing time and the smaller of the two wins.
 */
export const DEFAULT_CONCURRENCY = 8;

export type TransferPhase = 'idle' | 'preparing' | 'transferring' | 'paused' | 'done' | 'cancelled';

export type TransferSnapshot = {
  phase: TransferPhase;
  items: TransferItem[];
  /** Assets skipped because this receiver already has them. */
  alreadyThere: number;
  bytesSent: number;
  bytesTotal: number;
  filesDone: number;
  filesTotal: number;
  /** Bytes per second, rolling. */
  rate: number;
  /** Seconds, or undefined while there is nothing to base it on. */
  eta?: number;
  /** Milliseconds spent resolving assets before any byte moved. */
  prepareMs: number;
  /** Milliseconds of transfer. */
  transferMs: number;
  errors: string[];
};

export type TransferOptions = {
  receiver: Receiver;
  concurrency?: number;
  /** Which parts of each asset to send. Defaults to sending the most. */
  send?: SendOptions;
  onChange: (snapshot: TransferSnapshot) => void;
};

export class Transfer {
  private readonly receiver: Receiver;
  private readonly concurrency: number;
  private readonly sendOptions: SendOptions;
  private readonly onChange: (snapshot: TransferSnapshot) => void;

  private items: TransferItem[] = [];
  private byUploadId = new Map<string, TransferItem>();
  private queue?: WorkQueue<TransferItem>;
  private meter = new ThroughputMeter();
  private subscriptions: { remove: () => void }[] = [];

  private phase: TransferPhase = 'idle';
  private baseUrl = '';
  private alreadyThere = 0;
  private prepareMs = 0;
  private transferStartedAt?: number;
  private transferMs = 0;
  private errors: string[] = [];

  constructor(options: TransferOptions) {
    this.receiver = options.receiver;
    this.concurrency = Math.min(Math.max(options.concurrency ?? DEFAULT_CONCURRENCY, 1), 8);
    this.sendOptions = options.send ?? DEFAULT_SEND_OPTIONS;
    this.onChange = options.onChange;
  }

  /**
   * Runs a whole transfer: prepare, then send. Resolves when it has finished.
   *
   * Every copy this made is deleted on the way out, by whichever way it
   * leaves. A copy made to reach a Live Photo's video is tens of megabytes,
   * and a cancelled run that kept them would fill the phone with a second copy
   * of a library it never sent.
   */
  async run(summaries: AssetSummary[]): Promise<TransferSnapshot> {
    let resolved: Asset[] = [];
    try {
      this.baseUrl = await connect(this.receiver);

      resolved = await this.prepare(summaries);
      if (this.phase === 'cancelled') return this.snapshot();

      const plan = buildPlan(resolved, { alreadySent: await sentKeys(this.receiver.deviceId) });
      this.alreadyThere = plan.skipped.length;
      this.items = plan.items.map((asset) => ({ asset, state: 'queued', bytesSent: 0 }));
      this.emit();

      await this.transfer();
      return this.snapshot();
    } finally {
      // `resolved` and not `this.items`: an asset the receiver already had
      // was still copied out of the library to be looked at, and it is
      // skipped rather than sent, so nothing else would ever delete it.
      for (const asset of resolved) release(asset);
    }
  }

  pause(): void {
    if (this.phase !== 'transferring') return;
    this.setPhase('paused');
    this.queue?.pause();
    this.emit();
  }

  resume(): void {
    if (this.phase !== 'paused') return;
    this.setPhase('transferring');
    this.queue?.resume();
    this.emit();
  }

  cancel(): void {
    this.setPhase('cancelled');
    this.queue?.cancel();
    void GedaTransfer.cancelAll();
    this.emit();
  }

  snapshot(): TransferSnapshot {
    const now = Date.now();
    const bytesSent = this.items.reduce((sum, item) => sum + item.bytesSent, 0);
    const bytesTotal = this.items.reduce((sum, item) => sum + item.asset.size, 0);
    const filesDone = this.items.filter(
      (item) => item.state === 'done' || item.state === 'skipped',
    ).length;

    return {
      phase: this.phase,
      items: this.items,
      alreadyThere: this.alreadyThere,
      bytesSent,
      bytesTotal,
      filesDone,
      filesTotal: this.items.length,
      rate: this.meter.rate(now),
      eta: this.meter.eta(bytesTotal - bytesSent, now),
      prepareMs: this.prepareMs,
      transferMs: this.transferMs || this.elapsedTransfer(now),
      errors: this.errors,
    };
  }

  // MARK: preparation

  /** Turns library entries into assets with a real file behind them. */
  private async prepare(summaries: AssetSummary[]): Promise<Asset[]> {
    this.setPhase('preparing');
    this.emit();

    const startedAt = Date.now();
    // One summary can resolve to several files -- a Live Photo is a still and
    // a video -- so the results are flattened rather than paired up.
    const resolved = await mapPool(summaries, (summary) => resolveAsset(summary, this.sendOptions), {
      shouldContinue: () => this.phase === 'preparing',
      onError: (error, summary) => this.note(error, summary.filename),
    });

    this.prepareMs = Date.now() - startedAt;
    return resolved.flat();
  }

  // MARK: transfer

  private async transfer(): Promise<void> {
    if (this.items.length === 0) {
      this.setPhase('done');
      this.emit();
      return;
    }

    this.setPhase('transferring');
    this.transferStartedAt = Date.now();
    this.meter.start(this.transferStartedAt);

    this.subscriptions = [
      GedaTransfer.addListener('onUploadProgress', (event) => this.onProgress(event)),
      // Recorded as soon as the receiver hands out a URL, so that pausing
      // mid-file resumes from the offset rather than starting the file again.
      GedaTransfer.addListener('onUploadCreated', (event) => this.onCreated(event)),
    ];

    this.queue = new WorkQueue<TransferItem>((item, context) => this.send(item, context.signal), {
      limit: this.concurrency,
    });
    this.queue.add(this.items);

    // A steady tick so the rate and the ETA keep moving even while a single
    // large file is in flight and no item has changed state.
    const ticker = setInterval(() => this.emit(), 500);

    try {
      await this.queue.whenIdle();
    } finally {
      clearInterval(ticker);
      for (const subscription of this.subscriptions) subscription.remove();
      this.subscriptions = [];
    }

    const now = Date.now();
    this.meter.stop(now);
    this.transferMs = this.elapsedTransfer(now);

    if (this.phase !== 'cancelled' && this.phase !== 'paused') {
      this.setPhase('done');
    }
    this.emit();
  }

  private async send(item: TransferItem, signal: AbortSignal): Promise<void> {
    if (item.state === 'done' || item.state === 'skipped') return;

    const uploadId = `${item.asset.id}-${item.asset.size}`;
    this.byUploadId.set(uploadId, item);
    item.state = 'uploading';
    this.emit();

    const onAbort = () => {
      void GedaTransfer.cancel(uploadId);
    };
    signal.addEventListener('abort', onAbort);

    try {
      const result = await GedaTransfer.upload({
        uploadId,
        baseUrl: this.baseUrl,
        pin: this.receiver.spki,
        token: this.receiver.token,
        filePath: item.asset.filePath,
        size: item.asset.size,
        metadata: uploadMetadata(item.asset),
        // Resumes where an interrupted attempt stopped, rather than sending
        // the whole file again.
        location: item.location,
      });

      item.location = result.location;
      item.storedPath = result.storedPath;
      item.deduplicated = result.deduplicated;
      item.bytesSent = item.asset.size;
      item.state = result.deduplicated ? 'skipped' : 'done';

      await recordSent(this.receiver.deviceId, item.asset, result.storedPath);
    } catch (error) {
      if (signal.aborted || isAbort(error)) {
        // A pause or a cancel is not a failure, and showing it in red would
        // be a lie about what happened.
        item.state = this.phase === 'cancelled' ? 'cancelled' : 'queued';
        // The abort reason, not what the native side threw: the queue
        // recognises its own PausedError and puts the item back, and a
        // "cancelled by URLSession" in its place would quietly drop the file
        // the user was watching.
        throw signal.reason ?? error;
      }

      item.state = 'failed';
      item.error = message(error);
      this.note(error, item.asset.filename);
      throw error;
    } finally {
      signal.removeEventListener('abort', onAbort);
      this.byUploadId.delete(uploadId);
      this.emit();
    }
  }

  private onCreated(event: UploadCreatedEvent): void {
    const item = this.byUploadId.get(event.uploadId);
    if (item) item.location = event.location;
  }

  private onProgress(event: UploadProgressEvent): void {
    const item = this.byUploadId.get(event.uploadId);
    if (!item) return;

    const delta = event.bytesSent - item.bytesSent;
    item.bytesSent = event.bytesSent;
    if (delta > 0) this.meter.record(delta, Date.now());
  }

  /**
   * Assignment through a method on purpose: the compiler narrows a field to
   * the literal it was just assigned, and every later comparison in the same
   * function then looks impossible -- when in fact `cancel()` can change the
   * phase from another turn of the event loop while a transfer is in flight.
   */
  private setPhase(phase: TransferPhase): void {
    this.phase = phase;
  }

  private elapsedTransfer(now: number): number {
    if (this.transferStartedAt === undefined) return 0;
    return Math.max(now - this.transferStartedAt, 0);
  }

  private note(error: unknown, filename: string): void {
    const text =
      error instanceof AssetUnavailableError ? error.message : `${filename}: ${message(error)}`;
    // Keep the list bounded: a receiver that has gone away produces one error
    // per file, and a thousand identical lines help nobody.
    if (this.errors.length < 20 && !this.errors.includes(text)) {
      this.errors.push(text);
    }
  }

  private emit(): void {
    this.onChange(this.snapshot());
  }
}

function message(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}
