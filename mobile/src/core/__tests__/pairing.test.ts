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

import {
  PairingError,
  decodePairingPayload,
  isExpired,
  type PairingPayload,
} from '../pairing';

function encode(payload: Record<string, unknown>): string {
  const json = JSON.stringify(payload);
  const bytes = new TextEncoder().encode(json);
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return globalThis
    .btoa(binary)
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '');
}

const valid = {
  v: 1,
  device_id: 'receiver-1',
  name: 'Living Room NAS',
  spki: 'x2oWbPzmmlOFy/caNOvwd98jwAhXtGrdMCeYvMPRKIs=',
  addrs: ['192.168.1.10:47891', '[fd07::1]:47891'],
  psk: 'EUvS7CeyDi24d3xTcQGwBZGrTlKGa_i2JRksO9I8fqo',
  exp: 1786536436,
};

describe('decodePairingPayload', () => {
  it('reads a scanned code with or without the scheme', () => {
    const encoded = encode(valid);

    const withScheme = decodePairingPayload(`geda://pair/${encoded}`);
    const without = decodePairingPayload(encoded);

    expect(withScheme).toEqual<PairingPayload>(valid);
    expect(without).toEqual(withScheme);
  });

  it('keeps non-ASCII names intact', () => {
    // A receiver named in Vietnamese, Japanese, or with an emoji is ordinary.
    // Decoding base64 one character per byte would mangle all three.
    const payload = decodePairingPayload(encode({ ...valid, name: 'Máy chủ phòng khách 🏠' }));
    expect(payload.name).toBe('Máy chủ phòng khách 🏠');
  });

  it('rejects a payload with no key to pin', () => {
    // Without the pin there is nothing to trust on first use; accepting it
    // would be an unauthenticated connection wearing a QR code.
    expect(() => decodePairingPayload(encode({ ...valid, spki: '' }))).toThrow(PairingError);
  });

  it('rejects a payload with no secret or no addresses', () => {
    expect(() => decodePairingPayload(encode({ ...valid, psk: '' }))).toThrow(PairingError);
    expect(() => decodePairingPayload(encode({ ...valid, addrs: [] }))).toThrow(PairingError);
  });

  it('names the version when the two sides disagree', () => {
    const error = (() => {
      try {
        decodePairingPayload(encode({ ...valid, v: 2 }));
        return undefined;
      } catch (thrown) {
        return thrown as Error;
      }
    })();

    expect(error?.message).toContain('version 2');
  });

  it('rejects anything that is not a pairing code at all', () => {
    expect(() => decodePairingPayload('https://example.com')).toThrow(PairingError);
    expect(() => decodePairingPayload('')).toThrow(PairingError);
  });
});

describe('isExpired', () => {
  it('expires a code once its second has arrived', () => {
    // Short-lived on purpose: a photograph of the receiver's screen taken
    // during pairing must be worthless a few minutes later.
    expect(isExpired(valid, valid.exp * 1000 - 1)).toBe(false);
    expect(isExpired(valid, valid.exp * 1000)).toBe(true);
  });

  it('treats a missing expiry as no expiry', () => {
    expect(isExpired({ ...valid, exp: 0 }, Date.now())).toBe(false);
  });
});
