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

import { toJSON, toMarkdownRow, transferRate, wallClockRate, type BenchmarkRun } from '../benchmark';

const run: BenchmarkRun = {
  date: '2026-08-12',
  device: 'iPhone 17 Pro',
  osVersion: '27.0',
  appVersion: '1.0.0',
  receiver: 'gedad in Docker, NAS',
  link: 'Wi-Fi 6, 5 GHz',
  files: 201,
  bytes: 6_000_000_000,
  prepareMs: 30_000,
  transferMs: 90_000,
  concurrency: 8,
};

describe('benchmark arithmetic', () => {
  it('separates the transfer from the wait before it', () => {
    // Two numbers rather than one, because a transfer rate that looks
    // excellent next to a much lower wall-clock rate is the signature of the
    // PHAsset export being the bottleneck -- which is the thing to optimise
    // first (AGENTS.md §5).
    expect(transferRate(run)).toBeCloseTo(66_666_666, -4);
    expect(wallClockRate(run)).toBeCloseTo(50_000_000, -4);
  });

  it('has no opinion when nothing was measured', () => {
    expect(transferRate({ ...run, transferMs: 0 })).toBe(0);
    expect(wallClockRate({ ...run, transferMs: 0, prepareMs: 0 })).toBe(0);
  });
});

describe('recording', () => {
  it('produces a row that says what it was measured on', () => {
    // A number without the device, the link, and the receiver is not a
    // baseline, and every later phase is compared against this one.
    const row = toMarkdownRow(run);

    expect(row).toContain('iPhone 17 Pro');
    expect(row).toContain('Wi-Fi 6, 5 GHz');
    expect(row).toContain('gedad in Docker, NAS');
    expect(row).toContain('66.7');
    expect(row).toContain('50.0');
    expect(row.startsWith('| ')).toBe(true);
    expect(row.endsWith(' |')).toBe(true);
  });

  it('carries the same figures in the machine-readable form', () => {
    const parsed = JSON.parse(toJSON(run)) as Record<string, unknown>;
    expect(parsed.transferMBps).toBe(66.7);
    expect(parsed.wallClockMBps).toBe(50);
    expect(parsed.device).toBe('iPhone 17 Pro');
  });
});
