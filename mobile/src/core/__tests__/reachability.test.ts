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

import { diagnoseUnreachable, isLocalAddress } from '../reachability';

describe('isLocalAddress', () => {
  it('recognises the ranges a home network hands out', () => {
    expect(isLocalAddress('192.168.1.10:47891')).toBe(true);
    expect(isLocalAddress('10.0.0.4:47891')).toBe(true);
    expect(isLocalAddress('172.16.3.9:47891')).toBe(true);
    expect(isLocalAddress('172.31.255.254:47891')).toBe(true);
    expect(isLocalAddress('169.254.7.7:47891')).toBe(true);
    expect(isLocalAddress('127.0.0.1:47891')).toBe(true);
    expect(isLocalAddress('nas.local:47891')).toBe(true);
  });

  it('does not mistake a neighbouring range for a private one', () => {
    // 172.15 and 172.32 are both public; only 172.16-31 is private.
    expect(isLocalAddress('172.15.0.1:47891')).toBe(false);
    expect(isLocalAddress('172.32.0.1:47891')).toBe(false);
    expect(isLocalAddress('9.255.255.255:47891')).toBe(false);
    expect(isLocalAddress('193.168.1.10:47891')).toBe(false);
  });

  it('reads IPv6, bracketed and with a zone', () => {
    expect(isLocalAddress('[fe80::1%en0]:47891')).toBe(true);
    expect(isLocalAddress('[fd07::1]:47891')).toBe(true);
    expect(isLocalAddress('[::1]:47891')).toBe(true);
    expect(isLocalAddress('[2001:db8::1]:47891')).toBe(false);
  });

  it('does not count a tunnel as local', () => {
    // A connection over a VPN is exempt from the Local Network prompt, so
    // blaming the permission for one would send the user to the wrong screen.
    expect(isLocalAddress('100.101.102.103:47891')).toBe(false);
    expect(isLocalAddress('[fd7a:115c:a1e0::1]:47891')).toBe(false);
  });

  it('treats an unknown hostname as unknown, not local', () => {
    expect(isLocalAddress('desktop.example.com:47891')).toBe(false);
    expect(isLocalAddress('desktop:47891')).toBe(false);
  });

  it('accepts a full base URL as readily as host:port', () => {
    expect(isLocalAddress('https://192.168.1.10:47891')).toBe(true);
    expect(isLocalAddress('https://192.168.1.10:47891/v1/info')).toBe(true);
    expect(isLocalAddress('https://[fe80::1]:47891')).toBe(true);
  });

  it('says no rather than throwing on nonsense', () => {
    expect(isLocalAddress('')).toBe(false);
    expect(isLocalAddress('   ')).toBe(false);
    expect(isLocalAddress('[fe80::1')).toBe(false);
    expect(isLocalAddress('999.1.1.1:1')).toBe(false);
    expect(isLocalAddress('1.2.3:1')).toBe(false);
  });
});

describe('diagnoseUnreachable', () => {
  it('names the Local Network permission on a phone that has never reached one', () => {
    const result = diagnoseUnreachable('Studio NAS', {
      candidates: ['192.168.1.10:47891', '[fe80::1%en0]:47891'],
      everReachedLocally: false,
    });

    expect(result.reason).toBe('local-network');
    expect(result.offerSettings).toBe(true);
    expect(result.message).toContain('Studio NAS');
    expect(result.message).toContain('Local Network');
  });

  it('stops blaming the permission once it has demonstrably been granted', () => {
    // A local address has answered before, so the prompt was accepted. The
    // receiver being off is now the likelier story.
    const result = diagnoseUnreachable('Studio NAS', {
      candidates: ['192.168.1.10:47891'],
      everReachedLocally: true,
    });

    expect(result.reason).toBe('no-answer');
    expect(result.offerSettings).toBe(false);
  });

  it('does not blame the permission when a tunnel was in the set', () => {
    // One VPN address among the candidates means the race was not decided by
    // the permission alone.
    const result = diagnoseUnreachable('Studio NAS', {
      candidates: ['192.168.1.10:47891', '100.101.102.103:47891'],
      everReachedLocally: false,
    });

    expect(result.reason).toBe('no-answer');
    expect(result.offerSettings).toBe(false);
    expect(result.message).toContain('tunnel');
  });

  it('says so when there was nothing to try', () => {
    const result = diagnoseUnreachable('Studio NAS', {
      candidates: [],
      everReachedLocally: false,
    });

    expect(result.reason).toBe('no-addresses');
    expect(result.offerSettings).toBe(false);
  });
});
