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
 * The phase gate, in a form that can be pasted into the repository.
 *
 * P4's gate is a measurement, not a feature: MB/s over 200 mixed photos and a
 * 4K video, recorded in `docs/PERFORMANCE.md` (docs/PLAN.md). Everything after
 * it is compared against that number, so the run has to record what it was
 * measured on -- a figure without the device, the link, and the receiver is
 * not a baseline, it is a rumour.
 */

export type BenchmarkRun = {
  /** ISO date of the run. */
  date: string;
  device: string;
  osVersion: string;
  appVersion: string;
  receiver: string;
  /** How the phone was connected, entered by whoever ran it: "Wi-Fi 6, 5 GHz". */
  link: string;
  files: number;
  bytes: number;
  /** Milliseconds spent resolving assets before any byte moved. */
  prepareMs: number;
  /** Milliseconds of transfer. */
  transferMs: number;
  concurrency: number;
  notes?: string;
};

/** Bytes per second over the transfer alone. */
export function transferRate(run: BenchmarkRun): number {
  if (run.transferMs <= 0) return 0;
  return (run.bytes * 1000) / run.transferMs;
}

/**
 * Bytes per second over the whole operation, preparation included.
 *
 * This is the honest number: the user waits for both. A transfer rate that
 * looks excellent next to a wall-clock rate half its size is the signature of
 * the export being the bottleneck, which is exactly what the ordering in
 * AGENTS.md §5 predicts and what a later phase would go after.
 */
export function wallClockRate(run: BenchmarkRun): number {
  const total = run.prepareMs + run.transferMs;
  if (total <= 0) return 0;
  return (run.bytes * 1000) / total;
}

function mbPerSecond(bytesPerSecond: number): string {
  return (bytesPerSecond / 1_000_000).toFixed(1);
}

/** One row for the table in docs/PERFORMANCE.md. */
export function toMarkdownRow(run: BenchmarkRun): string {
  const cells = [
    run.date,
    run.device,
    `iOS ${run.osVersion}`,
    run.link,
    run.receiver,
    String(run.files),
    (run.bytes / 1_000_000_000).toFixed(2),
    String(run.concurrency),
    (run.prepareMs / 1000).toFixed(1),
    (run.transferMs / 1000).toFixed(1),
    mbPerSecond(transferRate(run)),
    mbPerSecond(wallClockRate(run)),
    run.notes ?? '',
  ];
  return `| ${cells.join(' | ')} |`;
}

export function toJSON(run: BenchmarkRun): string {
  return JSON.stringify(
    {
      ...run,
      transferMBps: Number(mbPerSecond(transferRate(run))),
      wallClockMBps: Number(mbPerSecond(wallClockRate(run))),
    },
    null,
    2,
  );
}
