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

// The user's preferences.
//
// Every one of them works with zero configuration and every destructive one is
// off (AGENTS.md §4). Two groups so far: where files that arrive from a
// computer are put, and which parts of each photo are sent to one.

import { DEFAULT_INBOX_SETTINGS, type InboxSettings } from '../core/inbox';
import { DEFAULT_SEND_OPTIONS, type SendOptions } from '../core/selection';
import { db } from './db';

const SAVE_MEDIA_TO_FILES = 'inbox.saveMediaToFiles';
const SEND_OPTIONS = 'send.options';
const DELETE_AFTER_TRANSFER = 'send.deleteAfterTransfer';

export async function loadInboxSettings(): Promise<InboxSettings> {
  try {
    const row = await (
      await db()
    ).getFirstAsync<{ value: string }>('SELECT value FROM settings WHERE key = ?', SAVE_MEDIA_TO_FILES);

    return { saveMediaToFiles: row?.value === '1' };
  } catch {
    // A preference that cannot be read is a preference at its default, which
    // is the safe one: media goes where the user expects to find it.
    return DEFAULT_INBOX_SETTINGS;
  }
}

export async function setSaveMediaToFiles(value: boolean): Promise<void> {
  await put(SAVE_MEDIA_TO_FILES, value ? '1' : '0');
}

/**
 * Which parts of each asset to send.
 *
 * Stored as one JSON value rather than a column per option: this is a group of
 * choices the user makes together, and a half-written group is worse than none
 * of it. Every field is merged over the defaults on read, so a preference file
 * written by an older version gains the new options at their defaults rather
 * than turning them off.
 */
export async function loadSendOptions(): Promise<SendOptions> {
  try {
    const row = await (
      await db()
    ).getFirstAsync<{ value: string }>('SELECT value FROM settings WHERE key = ?', SEND_OPTIONS);
    if (!row?.value) return DEFAULT_SEND_OPTIONS;
    return mergeSendOptions(JSON.parse(row.value));
  } catch {
    // Unreadable or unparseable: back up everything, which is the answer that
    // cannot lose a photo.
    return DEFAULT_SEND_OPTIONS;
  }
}

export async function setSendOptions(options: SendOptions): Promise<void> {
  await put(SEND_OPTIONS, JSON.stringify(options));
}

/**
 * Whether to delete an asset from this phone once the computer has proved it
 * holds it (docs/PLAN.md P9).
 *
 * Off, and it reads as off in every case that is not an explicit, stored
 * "1" -- an unreadable preference, a half-written value, a database from a
 * newer version. Everything else in this file falls back to a default that
 * costs bandwidth when it is wrong. This one falls back to the only default
 * that cannot cost a photograph.
 */
export async function loadDeleteAfterTransfer(): Promise<boolean> {
  try {
    const row = await (
      await db()
    ).getFirstAsync<{ value: string }>(
      'SELECT value FROM settings WHERE key = ?',
      DELETE_AFTER_TRANSFER,
    );
    return row?.value === '1';
  } catch {
    return false;
  }
}

export async function setDeleteAfterTransfer(value: boolean): Promise<void> {
  await put(DELETE_AFTER_TRANSFER, value ? '1' : '0');
}

/** Takes only the fields it recognises, at values it recognises. */
export function mergeSendOptions(stored: unknown): SendOptions {
  const out = { ...DEFAULT_SEND_OPTIONS };
  if (typeof stored !== 'object' || stored === null) return out;
  const raw = stored as Record<string, unknown>;

  if (typeof raw.livePhotoVideo === 'boolean') out.livePhotoVideo = raw.livePhotoVideo;
  if (typeof raw.screenshots === 'boolean') out.screenshots = raw.screenshots;
  if (typeof raw.hidden === 'boolean') out.hidden = raw.hidden;
  if (raw.edits === 'edited' || raw.edits === 'original' || raw.edits === 'both') {
    out.edits = raw.edits;
  }
  if (raw.raw === 'both' || raw.raw === 'raw' || raw.raw === 'jpeg') out.raw = raw.raw;
  if (raw.bursts === 'picks' || raw.bursts === 'all') out.bursts = raw.bursts;
  return out;
}

async function put(key: string, value: string): Promise<void> {
  await (
    await db()
  ).runAsync(
    `INSERT INTO settings (key, value) VALUES (?, ?)
     ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
    key,
    value,
  );
}
