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

// Decoding the QR code a receiver shows (docs/PROTOCOL.md §3.1).
//
// This is the moment trust is established for the whole life of the pairing:
// whatever SPKI pin comes out of here is the only thing that will ever be
// accepted from that receiver again. So a payload missing any of its parts is
// rejected outright rather than half-used.

export const PAIRING_VERSION = 1;
export const PAIRING_URI_SCHEME = 'geda://pair/';

export type PairingPayload = {
  v: number;
  device_id: string;
  name: string;
  /** base64(SHA-256(SubjectPublicKeyInfo)). */
  spki: string;
  /** Every address the receiver has, `host:port`, tunnels included. */
  addrs: string[];
  /** Single-use pairing secret. */
  psk: string;
  /** Unix seconds. */
  exp: number;
};

export class PairingError extends Error {}

/** Decodes a scanned code, with or without the `geda://pair/` prefix. */
export function decodePairingPayload(scanned: string): PairingPayload {
  const trimmed = scanned.trim();
  const encoded = trimmed.startsWith(PAIRING_URI_SCHEME)
    ? trimmed.slice(PAIRING_URI_SCHEME.length)
    : trimmed;

  let parsed: unknown;
  try {
    parsed = JSON.parse(fromBase64Url(encoded));
  } catch {
    throw new PairingError('That is not a Geda Transfer pairing code.');
  }

  const payload = parsed as Partial<PairingPayload>;
  if (payload.v !== PAIRING_VERSION) {
    throw new PairingError(
      `This code is version ${String(payload.v)}; this app speaks version ${PAIRING_VERSION}. Update whichever side is older.`,
    );
  }
  if (!payload.device_id) throw new PairingError('The code names no receiver.');
  if (!payload.spki) {
    // Without the pin there is nothing to trust on first use, and the
    // connection would be an unauthenticated one wearing a QR code.
    throw new PairingError('The code carries no key to pin.');
  }
  if (!payload.psk) throw new PairingError('The code carries no pairing secret.');
  if (!payload.addrs?.length) throw new PairingError('The code carries no addresses.');

  return {
    v: payload.v,
    device_id: payload.device_id,
    name: payload.name ?? payload.device_id,
    spki: payload.spki,
    addrs: payload.addrs,
    psk: payload.psk,
    exp: payload.exp ?? 0,
  };
}

/** A pairing code is short-lived so that a photograph of a screen is worthless. */
export function isExpired(payload: PairingPayload, nowMs: number = Date.now()): boolean {
  return payload.exp !== 0 && nowMs / 1000 >= payload.exp;
}

/**
 * Turns a `host:port` candidate into a base URL.
 *
 * IPv6 addresses arrive already bracketed from the receiver; anything else is
 * left alone so that a hostname works as well as an address.
 */
export function baseUrl(addr: string): string {
  return `https://${addr}`;
}

function fromBase64Url(value: string): string {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/');
  const withPadding = padded + '='.repeat((4 - (padded.length % 4)) % 4);
  return decodeBase64(withPadding);
}

/**
 * Base64 to a UTF-8 string.
 *
 * `atob` produces one character per byte, so a receiver named with anything
 * outside ASCII -- which is most of the world -- needs the bytes reassembled
 * before they are read as text.
 */
function decodeBase64(value: string): string {
  const binary = globalThis.atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
}
