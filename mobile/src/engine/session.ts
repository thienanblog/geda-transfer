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

// Finding a paired receiver, and pairing with a new one.

import GedaTransfer from '../../modules/geda-transfer';
import { baseUrl, isExpired, type PairingPayload } from '../core/pairing';
import { diagnoseUnreachable, isLocalAddress } from '../core/reachability';
import type { Receiver } from '../core/types';
import { loadReceivers, rememberAddr } from '../data/receivers';
import type { SelfIdentity } from '../data/receivers';

const CONNECT_TIMEOUT_MS = 4000;

export class ConnectError extends Error {
  /**
   * Whether to offer the user a jump to Settings.
   *
   * Set when the failure looks like a declined Local Network permission,
   * which is the one cause of silence that cannot be found from inside the
   * app (src/core/reachability.ts).
   */
  readonly offerSettings: boolean;

  constructor(message: string, offerSettings = false) {
    super(message);
    this.offerSettings = offerSettings;
  }
}

/**
 * Finds an address that works, out of everything the receiver advertised.
 *
 * A receiver advertises every address it has, VPN addresses included, and only
 * the phone can find out which of them is reachable from where it is standing:
 * the same desktop is 192.168.1.10 at home and 10.8.0.1 over WireGuard. They
 * are raced in parallel and the first to answer wins (AGENTS.md §3.4).
 */
export async function connect(receiver: Receiver): Promise<string> {
  const candidates = orderCandidates(receiver);
  const winner = await GedaTransfer.race(candidates, receiver.spki, CONNECT_TIMEOUT_MS);

  if (!winner) {
    throw await unreachable(receiver.name, candidates);
  }

  const addr = winner.replace(/^https:\/\//, '');
  void rememberAddr(receiver.deviceId, addr);
  return winner;
}

/** The address that worked last time first; it is the one most likely to work now. */
export function orderCandidates(receiver: Receiver): string[] {
  const seen = new Set<string>();
  const ordered: string[] = [];

  for (const addr of [receiver.lastGoodAddr, ...receiver.addrs]) {
    if (!addr || seen.has(addr)) continue;
    seen.add(addr);
    ordered.push(baseUrl(addr));
  }
  return ordered;
}

export type PairResponse = {
  token: string;
  device_id: string;
  name: string;
  spki: string;
  addrs: string[];
  naming_template: string;
  max_concurrency: number;
};

/**
 * Redeems a scanned pairing code.
 *
 * The pin from the QR code is used for this very first connection, so the
 * secret is never handed to anything but the key the user vouched for by
 * standing in front of the receiver.
 */
export async function pair(payload: PairingPayload, self: SelfIdentity): Promise<Receiver> {
  if (isExpired(payload)) {
    throw new ConnectError('That pairing code has expired. Show a fresh one on the receiver.');
  }

  const candidates = payload.addrs.map(baseUrl);
  const winner = await GedaTransfer.race(candidates, payload.spki, CONNECT_TIMEOUT_MS);
  if (!winner) {
    // The first connection this app ever makes is the one iOS puts the Local
    // Network prompt in front of, so pairing is where a declined permission
    // is most likely to be met -- and where it is least likely to be guessed.
    throw await unreachable(payload.name, candidates);
  }

  const response = await GedaTransfer.request({
    url: `${winner}/v1/pair`,
    method: 'POST',
    pin: payload.spki,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      v: payload.v,
      psk: payload.psk,
      device_id: self.deviceId,
      name: self.name,
      platform: 'ios',
    }),
  });

  if (response.status !== 200) {
    throw new ConnectError(describe(response.body, response.status));
  }

  const result = JSON.parse(response.body) as PairResponse;
  if (result.device_id !== payload.device_id) {
    // The pin already proved which key answered; this catches a receiver
    // serving a different identity behind the same key.
    throw new ConnectError('That receiver is not the one the code came from.');
  }

  return {
    deviceId: result.device_id,
    name: result.name || payload.name,
    spki: payload.spki,
    // The receiver knows its own addresses better than a QR code printed a
    // moment ago: a VPN may have come up since.
    addrs: result.addrs?.length ? result.addrs : payload.addrs,
    token: result.token,
    pairedAt: Date.now(),
    lastGoodAddr: winner.replace(/^https:\/\//, ''),
  };
}

/**
 * Builds the error for a race nobody won.
 *
 * `everReachedLocally` is read from the paired receivers rather than kept as a
 * flag of its own: an address that answered is recorded there already, and a
 * local one among them is proof the permission was granted at least once.
 */
async function unreachable(name: string, candidates: string[]): Promise<ConnectError> {
  let everReachedLocally = false;
  try {
    everReachedLocally = (await loadReceivers()).some(
      (entry) => entry.lastGoodAddr !== undefined && isLocalAddress(entry.lastGoodAddr),
    );
  } catch {
    // Unreadable storage is not worth a second failure on top of the first;
    // the diagnosis is only ever a hint.
  }

  const diagnosis = diagnoseUnreachable(name, { candidates, everReachedLocally });
  return new ConnectError(diagnosis.message, diagnosis.offerSettings);
}

function describe(body: string, status: number): string {
  try {
    const parsed = JSON.parse(body) as { message?: string };
    if (parsed.message) return parsed.message;
  } catch {
    // Not a protocol error document; fall through.
  }
  return `The receiver refused the pairing code (HTTP ${status}).`;
}
