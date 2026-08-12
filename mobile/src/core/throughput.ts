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
 * Throughput measurement.
 *
 * Speed is the headline feature, so it is measured rather than guessed
 * (AGENTS.md §5). Two numbers come out of here: a rolling rate, which is what
 * the progress screen shows and what the ETA is built from, and an overall
 * average, which is what goes in the repository as the phase gate.
 */

const DEFAULT_WINDOW_MS = 3000;

export type ThroughputSample = {
  at: number;
  bytes: number;
};

export class ThroughputMeter {
  private samples: ThroughputSample[] = [];
  private totalBytes = 0;
  private startedAt?: number;
  private endedAt?: number;

  constructor(private readonly windowMs: number = DEFAULT_WINDOW_MS) {}

  start(now: number): void {
    this.startedAt = now;
    this.endedAt = undefined;
  }

  stop(now: number): void {
    this.endedAt = now;
  }

  /** Records bytes that have arrived since the last call. */
  record(bytes: number, now: number): void {
    if (bytes <= 0) return;
    if (this.startedAt === undefined) this.startedAt = now;

    this.totalBytes += bytes;
    this.samples.push({ at: now, bytes });
    this.trim(now);
  }

  /**
   * Bytes per second over the recent window.
   *
   * A rolling window rather than the overall average, because the average
   * takes minutes to reflect a link that just got slower -- and a progress
   * screen that lies about the current speed is worse than one with no number.
   */
  rate(now: number): number {
    this.trim(now);
    if (this.samples.length === 0) return 0;

    // The span is the window, not the gap between the surviving samples.
    // Dividing by that gap would divide by nearly zero the moment a single
    // large chunk lands, and the screen would flash an impossible number --
    // "1.2 GB/s" on a Wi-Fi link -- every time a file completed.
    const started = this.startedAt ?? this.samples[0]!.at;
    const span = Math.min(this.windowMs, Math.max(now - started, 1));
    const bytes = this.samples.reduce((sum, sample) => sum + sample.bytes, 0);
    return (bytes * 1000) / span;
  }

  /** Bytes per second across the whole transfer. This is the reportable figure. */
  averageRate(now: number = Date.now()): number {
    const elapsed = this.elapsedMs(now);
    if (elapsed <= 0) return 0;
    return (this.totalBytes * 1000) / elapsed;
  }

  elapsedMs(now: number = Date.now()): number {
    if (this.startedAt === undefined) return 0;
    return Math.max((this.endedAt ?? now) - this.startedAt, 0);
  }

  get bytes(): number {
    return this.totalBytes;
  }

  /** Seconds remaining, or undefined while there is nothing to base it on. */
  eta(remainingBytes: number, now: number): number | undefined {
    const rate = this.rate(now);
    if (rate <= 0 || remainingBytes <= 0) return undefined;
    return remainingBytes / rate;
  }

  private trim(now: number): void {
    const cutoff = now - this.windowMs;
    while (this.samples.length > 0 && this.samples[0]!.at < cutoff) {
      this.samples.shift();
    }
  }
}

export function formatBytes(bytes: number): string {
  if (bytes < 1000) return `${bytes} B`;
  const units = ['kB', 'MB', 'GB', 'TB'];
  let value = bytes / 1000;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`;
}

export function formatRate(bytesPerSecond: number): string {
  if (bytesPerSecond <= 0) return '—';
  return `${formatBytes(bytesPerSecond)}/s`;
}

export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—';
  const whole = Math.round(seconds);
  if (whole < 60) return `${whole}s`;
  const minutes = Math.floor(whole / 60);
  const rest = whole % 60;
  if (minutes < 60) return `${minutes}m ${rest}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}
