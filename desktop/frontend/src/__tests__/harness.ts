// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// A fake Go side, so the window can be tested without a receiver.
//
// The point of these tests is the half of P6's gate that the Go-level gate
// cannot reach: not "does the receiver work" but "does the window show a
// person what happened, and does it tell them what to do next". They run
// against the real view code and a stub of the bridge.

import { vi } from "vitest";
import type {
  Device,
  GoApp,
  HistoryEntry,
  PairCode,
  QueuedFile,
  SendResult,
  Settings,
  SettingsView,
  Snapshot,
  Status,
} from "../bridge";

export const sampleStatus: Status = {
  running: true,
  name: "Studio Mac",
  device_id: "receiver-1",
  fingerprint: "AA78 · B7AD · 327A · 080C",
  dest: "/Users/an/Pictures/Geda Transfer",
  state_dir: "/Users/an/Library/Application Support/geda",
  version: "test",
  addrs: ["192.168.1.10:47891"],
  port: 47891,
  paired_devices: 1,
  files: 2,
  bytes: 3_000_000,
  onboarded: true,
};

export const samplePairCode: PairCode = {
  svg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 33 33"><rect width="33" height="33" fill="#ffffff"/><path fill="#000000" d="M4 4h1v1h-1z"/></svg>',
  uri: "geda://pair/eyJ2IjoxfQ",
  fingerprint: "AA78 · B7AD · 327A · 080C",
  addrs: ["192.168.1.10:47891"],
  expires_at: new Date(Date.now() + 5 * 60_000).toISOString(),
};

export const sampleSettings: SettingsView = {
  name: "Studio Mac",
  dest: "/Users/an/Pictures/Geda Transfer",
  port: 47891,
  advertise: null,
  mdns: true,
  discovery: true,
  autostart: false,
  onboarded: true,
  template: "{device}/{yyyy}/{original_name}.{ext}",
  template_variables: ["yyyy", "MM", "original_name", "ext"],
  template_preview: "An's iPhone/2026/IMG_4021.HEIC",
  autostart_supported: true,
  default_template: "{device}/{yyyy}/{original_name}.{ext}",
};

export function sampleDevice(over: Partial<Device> = {}): Device {
  return {
    id: "phone-1",
    name: "An's iPhone",
    platform: "ios",
    paired_at: new Date(Date.now() - 86_400_000).toISOString(),
    last_seen_at: new Date(Date.now() - 60_000).toISOString(),
    revoked: false,
    files: 12,
    bytes: 40_000_000,
    queued: 0,
    ...over,
  };
}

export function queuedFile(over: Partial<QueuedFile> = {}): QueuedFile {
  return {
    id: "item-1",
    device_id: "phone-1",
    filename: "archive.zip",
    size: 2_000_000_000,
    kind: "file",
    state: "ready",
    queued_at: new Date().toISOString(),
    ...over,
  };
}

export function emptySnapshot(): Snapshot {
  return {
    active: [],
    recent: [],
    bytes_per_second: 0,
    active_bytes: 0,
    active_total: 0,
    updated_at: new Date().toISOString(),
  };
}

// Stub is the fake bridge, with every call spied so a test can assert what the
// window asked the Go side to do.
export interface Stub {
  go: GoApp;
  emit: (name: string, payload: unknown) => void;
}

export function install(overrides: Partial<GoApp> = {}): Stub {
  const listeners = new Map<string, Array<(...data: unknown[]) => void>>();

  const go: GoApp = {
    Status: vi.fn(async () => sampleStatus),
    Pair: vi.fn(async () => samplePairCode),
    CancelPairing: vi.fn(async () => {}),
    Devices: vi.fn(async (): Promise<Device[]> => []),
    Unpair: vi.fn(async () => {}),
    History: vi.fn(async (): Promise<HistoryEntry[]> => []),
    Transfers: vi.fn(async () => emptySnapshot()),
    Settings: vi.fn(async () => sampleSettings),
    SaveSettings: vi.fn(async (next: Settings) => ({ ...sampleSettings, ...next })),
    PreviewTemplate: vi.fn(async () => "An's iPhone/2026/IMG_4021.HEIC"),
    ChooseDestination: vi.fn(async () => ""),
    OpenDestination: vi.fn(async () => {}),
    RevealFile: vi.fn(async () => {}),
    ChooseAndSend: vi.fn(async (): Promise<SendResult> => ({ queued: [], cancelled: true })),
    Outbox: vi.fn(async (): Promise<QueuedFile[]> => []),
    CancelSend: vi.fn(async () => {}),
    ClearSent: vi.fn(async () => 0),
    FinishOnboarding: vi.fn(async () => {}),
    ...overrides,
  };

  window.go = { app: { App: go } };
  window.runtime = {
    EventsOn(name, callback) {
      const list = listeners.get(name) ?? [];
      list.push(callback);
      listeners.set(name, list);
      return () => {
        listeners.set(name, (listeners.get(name) ?? []).filter((c) => c !== callback));
      };
    },
    EventsOff(name) {
      listeners.delete(name);
    },
    BrowserOpenURL: vi.fn(),
  };

  return {
    go,
    emit(name, payload) {
      for (const callback of listeners.get(name) ?? []) callback(payload);
    },
  };
}

// settle waits for the promise chains a view kicks off in its constructor.
export async function settle(times = 4): Promise<void> {
  for (let i = 0; i < times; i++) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
}

export function text(node: Element): string {
  return node.textContent ?? "";
}
