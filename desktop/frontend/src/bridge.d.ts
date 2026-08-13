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
  queued: number;
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

export type Direction = "inbound" | "outbound";

export interface Transfer {
  upload_id: string;
  device_id: string;
  device_name: string;
  direction: Direction;
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
  output_preset: OutputPreset;
  /** Only meaningful when output_preset is "custom". */
  output_matrix?: Partial<Record<FileClass, OutputAction>> | null;
}

/** What the receiver does with the files it stores (docs/PLAN.md P8). */
export type OutputPreset = "original" | "compatible" | "space-saving" | "custom";

/** A family of received file, as far as conversion is concerned. */
export type FileClass = "heic" | "video" | "raw" | "other";

/**
 * keep    leave the file exactly as it arrived
 * sidecar write a converted copy beside it; both survive
 * replace write the converted copy and remove the original
 */
export type OutputAction = "keep" | "sidecar" | "replace";

export interface Tool {
  name: string;
  /** Empty when the tool is not installed. */
  path: string;
  version: string;
}

export interface Tools {
  ffmpeg: Tool;
  ffprobe: Tool;
  heif_convert: Tool;
}

export interface OutputView {
  presets: OutputPreset[];
  classes: FileClass[];
  actions: OutputAction[];
  /** What the chosen preset resolves to, per class. */
  effective: Record<string, OutputAction>;
  tools: Tools;
  /** Why nothing can be converted, for the *saved* preset. */
  unavailable: string;
  /**
   * What has to be installed to convert each class, for the classes this
   * machine cannot convert. Absent for a class it can.
   *
   * The window builds its own message from this, so the warning appears the
   * moment a converting preset is picked rather than after it is saved.
   */
  missing: Partial<Record<FileClass, string>>;
  /** How to install them on this platform. */
  install: string;
  pending: number;
}

export type ConversionState = "pending" | "running" | "done" | "skipped" | "failed";

export interface Conversion {
  ID: number;
  FileID: number;
  DeviceID: string;
  SourcePath: string;
  Class: FileClass;
  Action: OutputAction;
  State: ConversionState;
  OutputPath: string;
  OutputSize: number;
  Tool: string;
  Note: string;
  Error: string;
  QueuedAt: string;
  FinishedAt?: string | null;
}

export interface SettingsView extends Settings {
  template_variables: string[];
  template_preview: string;
  autostart_supported: boolean;
  default_template: string;
  output: OutputView;
}

// One file queued for a phone to collect. "queued" is not "sent": this
// computer cannot push to a suspended iPhone, so the file waits until the
// phone next asks (docs/PROTOCOL.md 6).
export interface QueuedFile {
  id: string;
  device_id: string;
  filename: string;
  size: number;
  kind: string;
  state: "pending" | "ready" | "claimed" | "delivered" | "failed";
  error?: string;
  queued_at: string;
  delivered_at?: string;
}

export interface SendResult {
  queued: QueuedFile[] | null;
  // The user closed the picker without choosing anything. Not an error.
  cancelled: boolean;
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
  ChooseAndSend(deviceID: string): Promise<SendResult>;
  Outbox(deviceID: string): Promise<QueuedFile[]>;
  CancelSend(deviceID: string, id: string): Promise<void>;
  ClearSent(deviceID: string): Promise<number>;
  Conversions(limit: number): Promise<Conversion[]>;
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
