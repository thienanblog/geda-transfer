// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// What this computer is holding for one phone to collect.
//
// The wording here does a lot of work. Nothing can push to a suspended iPhone
// (AGENTS.md 3.7), so pressing "Send files" does not send: it queues, and the
// bytes move the next time somebody opens the app on that phone. A panel that
// said "Sent" would be a lie that only surfaces hours later as a missing file.

import type { Device, QueuedFile } from "../bridge";
import { when } from "../format";
import { bytes } from "../format";
import { el, icon, on } from "../dom";

export interface OutboxActions {
  withdraw: (item: QueuedFile) => void;
  clearFinished: () => void;
}

// outboxSection renders the queue for one device, or nothing at all when there
// is none: an empty panel under every phone would be permanent furniture
// advertising a feature most of the time nobody is using.
export function outboxSection(
  device: Device,
  items: QueuedFile[],
  actions: OutboxActions,
): HTMLElement | undefined {
  if (items.length === 0) return undefined;

  const waiting = items.filter((item) => !isFinished(item));
  const finished = items.filter(isFinished);

  const header = el(
    "div",
    { class: "queue-header" },
    el("span", {
      class: "queue-title",
      text:
        waiting.length > 0
          ? `${waiting.length} waiting for ${device.name} to collect`
          : "Nothing waiting",
    }),
  );

  if (finished.length > 0) {
    const clear = el("button", { class: "link", type: "button" }, "Clear finished");
    on(clear, "click", actions.clearFinished);
    header.append(clear);
  }

  return el("div", { class: "queue" }, header, ...items.map((item) => row(item, actions)));
}

function row(item: QueuedFile, actions: OutboxActions): HTMLElement {
  const kindIcon = item.kind === "photo" ? "photo" : item.kind === "video" ? "video" : "file";

  const withdraw = el(
    "button",
    { class: "button button-quiet", type: "button" },
    "Withdraw",
  );
  on(withdraw, "click", () => actions.withdraw(item));

  return el(
    "div",
    { class: `queue-row ${item.state === "failed" ? "is-failed" : ""}` },
    el("div", { class: "queue-icon" }, icon(kindIcon)),
    el(
      "div",
      { class: "queue-main" },
      // textContent: a filename is text, wherever it came from.
      el("div", { class: "queue-name", text: item.filename }),
      el("div", { class: "queue-meta", text: `${bytes(item.size)} · ${describe(item)}` }),
    ),
    !isFinished(item) && withdraw,
  );
}

export function isFinished(item: QueuedFile): boolean {
  return item.state === "delivered" || item.state === "failed";
}

// describe says what is actually true of each state, in the terms somebody
// waiting for a file cares about: has the phone got it yet, and if not, why.
export function describe(item: QueuedFile): string {
  switch (item.state) {
    case "pending":
      return "Preparing…";
    case "ready":
      return "Waiting for the phone to open the app";
    case "claimed":
      return "Sending now";
    case "delivered":
      return `Delivered ${when(item.delivered_at)}`;
    case "failed":
      return item.error || "Failed";
    default:
      return item.state;
  }
}
