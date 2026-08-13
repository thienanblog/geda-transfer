// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// Numbers and dates, in the words a person uses.

// bytes formats a size in decimal units.
//
// Decimal, not binary: this app's numbers are compared against a network link,
// and links are sold in megabits and megabytes of a thousand.
export function bytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  if (n < 1000) return `${Math.round(n)} B`;

  const units = ["kB", "MB", "GB", "TB", "PB"];
  let value = n / 1000;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit++;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

export function rate(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return "";
  return `${bytes(bytesPerSecond)}/s`;
}

export function percent(done: number, total: number): number {
  if (!Number.isFinite(total) || total <= 0) return 0;
  return Math.max(0, Math.min(100, (done / total) * 100));
}

const dateTime = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

export function when(iso: string | undefined): string {
  if (!iso) return "—";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "—";
  return dateTime.format(at);
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

// ago renders a timestamp as "3 minutes ago", falling back to a date once the
// distance stops being something a person holds in their head.
export function ago(iso: string | undefined): string {
  if (!iso) return "never";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "never";

  const seconds = (at.getTime() - Date.now()) / 1000;
  const magnitude = Math.abs(seconds);

  if (magnitude < 45) return "just now";
  if (magnitude < 3600) return relative.format(Math.round(seconds / 60), "minute");
  if (magnitude < 86400) return relative.format(Math.round(seconds / 3600), "hour");
  if (magnitude < 7 * 86400) return relative.format(Math.round(seconds / 86400), "day");
  return dateTime.format(at);
}

// countdown renders the time left on a pairing code.
export function countdown(iso: string): string {
  const left = Math.max(0, new Date(iso).getTime() - Date.now());
  const total = Math.round(left / 1000);
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

export function plural(n: number, word: string, plural_ = `${word}s`): string {
  return `${n} ${n === 1 ? word : plural_}`;
}
