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

// The typed surface of the native transfer module.
//
// Everything here is a thin wrapper: the decisions live in `src/core`, which
// is plain TypeScript with no React Native imports and therefore testable
// without a device.

import { NativeModule, requireNativeModule } from 'expo';

import type { BackgroundJob } from '../../src/core/background';

export type UploadProgressEvent = {
  uploadId: string;
  bytesSent: number;
  totalBytes: number;
};

/** Emitted once the receiver has a URL for the upload, before any byte moves. */
export type UploadCreatedEvent = {
  uploadId: string;
  location: string;
};

/** Progress from the background session, while the app happens to be alive. */
export type BackgroundProgressEvent = {
  uploadId: string;
  bytesSent: number;
  totalBytes: number;
};

export type GedaTransferEvents = {
  onUploadProgress: (event: UploadProgressEvent) => void;
  onUploadCreated: (event: UploadCreatedEvent) => void;
  onBackgroundProgress: (event: BackgroundProgressEvent) => void;
  onBackgroundFinished: (job: BackgroundJob) => void;
};

/** One file for the background session. */
export type BackgroundRequest = {
  uploadId: string;
  receiverId: string;
  /** Shown on the Lock Screen by a process that cannot ask JavaScript. */
  receiverName: string;
  assetId: string;
  filename: string;
  baseUrl: string;
  pin: string;
  token: string;
  /**
   * A copy inside the app container, made by the caller.
   *
   * It cannot be a photo library path: the bytes are sent by a system process
   * that has no access to the library (see DECISIONS).
   */
  stagedPath: string;
  size: number;
  metadata: Record<string, string>;
};

export type RequestOptions = {
  /** Absolute URL, including the scheme and the port. */
  url: string;
  method?: string;
  /** base64(SHA-256(SubjectPublicKeyInfo)) of the receiver, from pairing. */
  pin: string;
  token?: string;
  headers?: Record<string, string>;
  body?: string;
};

export type RequestResult = {
  status: number;
  /** Header names are lower-cased. */
  headers: Record<string, string>;
  body: string;
};

export type UploadOptions = {
  /** Identifies this upload in progress events and in `cancel`. */
  uploadId: string;
  /** `https://host:port`, with no trailing slash. */
  baseUrl: string;
  pin: string;
  token: string;
  /** A path on disk. The bytes never enter JavaScript (AGENTS.md §3.8). */
  filePath: string;
  size: number;
  /** tus metadata; values are base64-encoded natively. */
  metadata: Record<string, string>;
  /** Set to resume an upload the receiver already knows about. */
  location?: string;
};

export type UploadResult = {
  location: string;
  status: number;
  bytesSent: number;
  /** Where the file landed, relative to the receiver's destination. */
  storedPath: string;
  /** The receiver already held this file and skipped writing it again. */
  deduplicated: boolean;
  resumedFrom: number;
};

declare class GedaTransferModule extends NativeModule<GedaTransferEvents> {
  request(options: RequestOptions): Promise<RequestResult>;

  /**
   * Races the candidate addresses and returns the first that answers, or null.
   *
   * A paired receiver advertises every address it has, VPN addresses included,
   * and only the phone can discover which of them works from where it is now.
   */
  race(urls: string[], pin: string, timeoutMs: number): Promise<string | null>;

  /** Creates or resumes a tus upload and sends the file. */
  upload(options: UploadOptions): Promise<UploadResult>;

  cancel(uploadId: string): Promise<void>;
  cancelAll(): Promise<void>;

  /** Where a staged copy must be written before it can be sent. */
  backgroundStagingDirectory(): string;

  /**
   * Creates the uploads and hands the files to the system.
   *
   * Returns the ids that were accepted. From this point the transfer belongs
   * to `nsurlsessiond` and continues whether or not this app is running.
   */
  startBackground(requests: BackgroundRequest[]): Promise<string[]>;

  /** Hands back anything the system stopped working on, resuming by offset. */
  reconcileBackground(): Promise<BackgroundJob[]>;

  /** The same, for jobs that failed outright. */
  retryBackground(): Promise<BackgroundJob[]>;

  backgroundJobs(): BackgroundJob[];

  /** Forgets delivered jobs, once they are in the ledger. Failures stay. */
  clearDeliveredBackground(): void;

  cancelBackground(): void;

  /** Asks the system for a wake-up on power and Wi-Fi. */
  scheduleBackgroundKickoff(): void;

  /** False when the user has turned Live Activities off for this app. */
  liveActivitiesAvailable(): boolean;
}

export default requireNativeModule<GedaTransferModule>('GedaTransfer');
