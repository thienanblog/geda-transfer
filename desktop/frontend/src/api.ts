// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The Go side, wrapped so the rest of the app never touches `window.go`.

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
} from "./bridge";

function backend(): GoApp {
  const go = window.go?.app?.App;
  if (!go) {
    throw new Error("The app is still starting.");
  }
  return go;
}

// HISTORY_PAGE is how many rows one page of history holds. The window compares
// what came back against it to decide whether there is more to fetch.
export const HISTORY_PAGE = 100;

export const api = {
  status: (): Promise<Status> => backend().Status(),
  pair: (): Promise<PairCode> => backend().Pair(),
  cancelPairing: (): Promise<void> => backend().CancelPairing(),
  devices: (): Promise<Device[]> => backend().Devices(),
  unpair: (id: string): Promise<void> => backend().Unpair(id),
  history: (deviceID = "", before = "", limit = HISTORY_PAGE): Promise<HistoryEntry[]> =>
    backend().History(deviceID, before, limit),
  transfers: (): Promise<Snapshot> => backend().Transfers(),
  settings: (): Promise<SettingsView> => backend().Settings(),
  saveSettings: (next: Settings): Promise<SettingsView> => backend().SaveSettings(next),
  previewTemplate: (template: string): Promise<string> => backend().PreviewTemplate(template),
  chooseDestination: (): Promise<string> => backend().ChooseDestination(),
  openDestination: (): Promise<void> => backend().OpenDestination(),
  revealFile: (storedPath: string): Promise<void> => backend().RevealFile(storedPath),
  chooseAndSend: (deviceID: string): Promise<SendResult> => backend().ChooseAndSend(deviceID),
  outbox: (deviceID: string): Promise<QueuedFile[]> => backend().Outbox(deviceID),
  cancelSend: (deviceID: string, id: string): Promise<void> => backend().CancelSend(deviceID, id),
  clearSent: (deviceID: string): Promise<number> => backend().ClearSent(deviceID),
  finishOnboarding: (): Promise<void> => backend().FinishOnboarding(),
};

// events subscribes to a push from Go, returning an unsubscribe function.
export function events<T>(name: string, handler: (payload: T) => void): () => void {
  const runtime = window.runtime;
  if (!runtime) return () => {};
  return runtime.EventsOn(name, (...data: unknown[]) => handler(data[0] as T));
}

// message turns whatever Go returned into something worth showing a person.
//
// A binding rejects with a string, an Error, or occasionally an object; the
// window must never end up displaying "[object Object]" over a real problem.
export function message(err: unknown): string {
  if (typeof err === "string") return err;
  if (err instanceof Error) return err.message;
  if (err && typeof err === "object" && "message" in err) {
    return String((err as { message: unknown }).message);
  }
  return "Something went wrong.";
}
