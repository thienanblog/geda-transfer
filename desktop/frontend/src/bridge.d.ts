// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The Go side, as the page sees it.
//
// These are written by hand rather than taken from `wails generate module`.
// The generated bindings are a build artefact of a tool that is not needed to
// typecheck or to run the tests, and a checked-in copy of them drifts. Writing
// the contract out once, here, means the compiler catches a rename on the Go
// side at `npm run typecheck` -- which is where it should be caught.

export interface Status {
  running: boolean;
  error?: string;
  name: string;
  device_id: string;
  fingerprint: string;
  dest: string;
  state_dir: string;
  version: string;
  addrs: string[] | null;
  port: number;
  paired_devices: number;
  files: number;
  bytes: number;
  started_at?: string;
  onboarded: boolean;
}

export interface PairCode {
  svg: string;
  uri: string;
  fingerprint: string;
  addrs: string[] | null;
  expires_at: string;
}

export interface Device {
  id: string;
  name: string;
  platform: string;
  paired_at: string;
  last_seen_at?: string;
  revoked: boolean;
  files: number;
  bytes: number;
}

export interface HistoryEntry {
  id: number;
  device_id: string;
  device_name: string;
  name: string;
  stored_path: string;
  kind: string;
  size: number;
  hash: string;
  captured_at?: string;
  received_at: string;
}

export type Outcome = "" | "stored" | "skipped" | "failed";

export interface Transfer {
  upload_id: string;
  device_id: string;
  device_name: string;
  name: string;
  kind: string;
  size: number;
  offset: number;
  started_at: string;
  ended_at?: string;
  outcome: Outcome;
  stored_path?: string;
  error?: string;
}

export interface Snapshot {
  active: Transfer[] | null;
  recent: Transfer[] | null;
  bytes_per_second: number;
  active_bytes: number;
  active_total: number;
  updated_at: string;
}

export interface Settings {
  name: string;
  dest: string;
  port: number;
  advertise: string[] | null;
  mdns: boolean;
  discovery: boolean;
  autostart: boolean;
  onboarded: boolean;
  template: string;
}

export interface SettingsView extends Settings {
  template_variables: string[];
  template_preview: string;
  autostart_supported: boolean;
  default_template: string;
}

export interface ReceiverEvent {
  running: boolean;
  error?: string;
}

export interface GoApp {
  Status(): Promise<Status>;
  Pair(): Promise<PairCode>;
  CancelPairing(): Promise<void>;
  Devices(): Promise<Device[]>;
  Unpair(deviceID: string): Promise<void>;
  History(deviceID: string, before: string, limit: number): Promise<HistoryEntry[]>;
  Transfers(): Promise<Snapshot>;
  Settings(): Promise<SettingsView>;
  SaveSettings(next: Settings): Promise<SettingsView>;
  PreviewTemplate(template: string): Promise<string>;
  ChooseDestination(): Promise<string>;
  OpenDestination(): Promise<void>;
  RevealFile(storedPath: string): Promise<void>;
  FinishOnboarding(): Promise<void>;
}

declare global {
  interface Window {
    go: { app: { App: GoApp } };
    runtime: {
      EventsOn(name: string, callback: (...data: unknown[]) => void): () => void;
      EventsOff(name: string): void;
      BrowserOpenURL(url: string): void;
    };
  }
}
