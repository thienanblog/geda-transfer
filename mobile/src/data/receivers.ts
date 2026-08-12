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

// Where paired receivers live.
//
// The token is a bearer credential and the pin is what stops a stranger from
// impersonating the receiver, so both go in the keychain rather than in
// ordinary storage. There is no account and no server: this file is the whole
// of what "signed in" means in this app.

import * as Device from 'expo-device';
import * as SecureStore from 'expo-secure-store';
import * as Crypto from 'expo-crypto';

import type { Receiver } from '../core/types';

const RECEIVERS_KEY = 'geda.receivers.v1';
const IDENTITY_KEY = 'geda.identity.v1';

export type SelfIdentity = {
  /** This phone's identifier, as the receiver files its uploads under. */
  deviceId: string;
  name: string;
};

export async function loadReceivers(): Promise<Receiver[]> {
  const raw = await SecureStore.getItemAsync(RECEIVERS_KEY);
  if (!raw) return [];
  try {
    return JSON.parse(raw) as Receiver[];
  } catch {
    // Unreadable storage is not worth crashing over: the user pairs again,
    // which takes ten seconds and is the documented recovery for everything
    // else that can go wrong with trust.
    return [];
  }
}

export async function saveReceiver(receiver: Receiver): Promise<Receiver[]> {
  const existing = await loadReceivers();
  const next = [receiver, ...existing.filter((r) => r.deviceId !== receiver.deviceId)];
  await SecureStore.setItemAsync(RECEIVERS_KEY, JSON.stringify(next));
  return next;
}

export async function forgetReceiver(deviceId: string): Promise<Receiver[]> {
  const next = (await loadReceivers()).filter((r) => r.deviceId !== deviceId);
  await SecureStore.setItemAsync(RECEIVERS_KEY, JSON.stringify(next));
  return next;
}

/**
 * Remembers which address answered, so the next connection tries it first.
 *
 * Racing the whole candidate set costs a round trip on every address that
 * does not answer; after the first success, one of them is known to work.
 */
export async function rememberAddr(deviceId: string, addr: string): Promise<void> {
  const receivers = await loadReceivers();
  const receiver = receivers.find((r) => r.deviceId === deviceId);
  if (!receiver || receiver.lastGoodAddr === addr) return;

  receiver.lastGoodAddr = addr;
  await SecureStore.setItemAsync(RECEIVERS_KEY, JSON.stringify(receivers));
}

/**
 * This phone's identity, minted once and kept.
 *
 * The receiver files uploads under this identifier, so a new one on every
 * launch would scatter one phone's photos across a row of "devices" and lose
 * the transfer history with it.
 */
export async function loadIdentity(): Promise<SelfIdentity> {
  const raw = await SecureStore.getItemAsync(IDENTITY_KEY);
  if (raw) {
    try {
      return JSON.parse(raw) as SelfIdentity;
    } catch {
      // Fall through and mint a new one.
    }
  }

  const identity: SelfIdentity = {
    deviceId: Crypto.randomUUID(),
    name: Device.deviceName ?? Device.modelName ?? 'iPhone',
  };
  await SecureStore.setItemAsync(IDENTITY_KEY, JSON.stringify(identity));
  return identity;
}
