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
// off (AGENTS.md §4). There is exactly one so far, and it is Advanced: whether
// photos and videos that arrive from a computer go to the Photo Library, where
// people look for them, or to this app's Files container, where they do not.

import { DEFAULT_INBOX_SETTINGS, type InboxSettings } from '../core/inbox';
import { db } from './db';

const SAVE_MEDIA_TO_FILES = 'inbox.saveMediaToFiles';

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
  await (
    await db()
  ).runAsync(
    `INSERT INTO settings (key, value) VALUES (?, ?)
     ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
    SAVE_MEDIA_TO_FILES,
    value ? '1' : '0',
  );
}
