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

import { ThroughputMeter, formatBytes, formatDuration, formatRate } from '../throughput';

describe('ThroughputMeter', () => {
  it('reports the overall average, which is the figure that gets recorded', () => {
    const meter = new ThroughputMeter();
    meter.start(0);
    meter.record(50_000_000, 1_000);
    meter.record(50_000_000, 2_000);
    meter.stop(2_000);

    // 100 MB in 2 seconds.
    expect(meter.averageRate(9_999)).toBeCloseTo(50_000_000, -3);
    expect(meter.elapsedMs(9_999)).toBe(2_000);
    expect(meter.bytes).toBe(100_000_000);
  });

  it('never reports a rate the link cannot reach', () => {
    // One large chunk arriving at once used to divide by the gap between
    // samples -- nearly zero -- and flash "1.2 GB/s" on a Wi-Fi transfer.
    const meter = new ThroughputMeter(3_000);
    meter.start(0);
    meter.record(10_000_000, 1_000);

    expect(meter.rate(1_000)).toBeCloseTo(10_000_000, -3);
  });

  it('follows a link that just got slower', () => {
    // A rolling window, because an overall average takes minutes to react and
    // a progress screen that lies about the current speed is worse than one
    // with no number at all.
    const meter = new ThroughputMeter(1_000);
    meter.start(0);
    meter.record(100_000_000, 100);

    const fast = meter.rate(200);
    const later = meter.rate(5_000);

    expect(fast).toBeGreaterThan(0);
    expect(later).toBe(0);
  });

  it('has no opinion before there is anything to measure', () => {
    const meter = new ThroughputMeter();
    expect(meter.rate(0)).toBe(0);
    expect(meter.averageRate(1_000)).toBe(0);
    expect(meter.eta(1_000_000, 0)).toBeUndefined();
  });

  it('estimates the remaining time from the recent rate', () => {
    const meter = new ThroughputMeter(10_000);
    meter.start(0);
    meter.record(10_000_000, 1_000);

    // 10 MB/s with 50 MB to go.
    expect(meter.eta(50_000_000, 1_000)).toBeCloseTo(5, 1);
  });
});

describe('formatting', () => {
  it('uses units a person reads at a glance', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1_500)).toBe('1.5 kB');
    expect(formatBytes(3_145_728)).toBe('3.1 MB');
    expect(formatRate(0)).toBe('—');
    expect(formatRate(12_500_000)).toBe('12.5 MB/s');
    expect(formatDuration(45)).toBe('45s');
    expect(formatDuration(125)).toBe('2m 5s');
    expect(formatDuration(3_725)).toBe('1h 2m');
    expect(formatDuration(Number.NaN)).toBe('—');
  });
});
