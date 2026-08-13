// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The live transfer view: what is arriving now, and what just landed.

import type { Snapshot, Status, Transfer } from "../bridge";
import { api, events, message } from "../api";
import { bytes, percent, plural, rate, when } from "../format";
import { el, icon, mount, on } from "../dom";

export function transfersView(status: Status, openPairing: () => void): {
  element: HTMLElement;
  destroy: () => void;
} {
  const summary = el("div", { class: "summary" });
  const list = el("div", { class: "transfer-list" });
  const element = el("section", { class: "view" }, summary, list);

  let latest: Snapshot | null = null;

  function render(snapshot: Snapshot): void {
    latest = snapshot;
    const active = snapshot.active ?? [];
    const recent = snapshot.recent ?? [];

    renderSummary(active, snapshot);

    if (active.length === 0 && recent.length === 0) {
      mount(list, empty(status, openPairing));
      return;
    }

    mount(
      list,
      active.length > 0 && section("Arriving now", active.map((t) => row(t, true))),
      recent.length > 0 && section("Just received", recent.map((t) => row(t, false))),
    );
  }

  function renderSummary(active: Transfer[], snapshot: Snapshot): void {
    if (active.length === 0) {
      mount(
        summary,
        el(
          "div",
          { class: "summary-idle" },
          el("span", { class: "dot dot-ready" }),
          el("span", {
            text: status.running ? "Ready to receive" : "Not receiving",
          }),
        ),
      );
      return;
    }

    const done = snapshot.active_bytes;
    const total = snapshot.active_total;
    const speed = rate(snapshot.bytes_per_second);

    mount(
      summary,
      el(
        "div",
        { class: "summary-active" },
        el(
          "div",
          { class: "summary-line" },
          el("strong", { text: `Receiving ${plural(active.length, "file")}` }),
          speed ? el("span", { class: "speed", text: speed }) : null,
        ),
        bar(percent(done, total)),
        el("div", {
          class: "muted",
          text: total > 0 ? `${bytes(done)} of ${bytes(total)}` : bytes(done),
        }),
      ),
    );
  }

  function section(title: string, rows: HTMLElement[]): HTMLElement {
    return el("div", { class: "group" }, el("h2", { class: "group-title", text: title }), ...rows);
  }

  function row(transfer: Transfer, active: boolean): HTMLElement {
    const kindIcon =
      transfer.kind === "photo" ? "photo" : transfer.kind === "video" ? "video" : "file";

    const meta: HTMLElement[] = [el("span", { text: transfer.device_name || "Unknown device" })];
    if (transfer.size > 0) meta.push(el("span", { text: bytes(transfer.size) }));

    const node = el(
      "div",
      { class: `transfer ${active ? "is-active" : ""}` },
      el("div", { class: "transfer-icon" }, icon(kindIcon)),
      el(
        "div",
        { class: "transfer-main" },
        // textContent, always: this name came off a phone.
        el("div", { class: "transfer-name", text: transfer.name }),
        el("div", { class: "transfer-meta" }, ...meta),
        active ? bar(percent(transfer.offset, transfer.size)) : null,
      ),
      outcome(transfer),
    );
    return node;
  }

  function outcome(transfer: Transfer): HTMLElement {
    if (!transfer.outcome) {
      return el("div", {
        class: "transfer-status",
        text: transfer.size > 0 ? `${Math.round(percent(transfer.offset, transfer.size))}%` : "",
      });
    }

    if (transfer.outcome === "failed") {
      return el("div", {
        class: "transfer-status is-failed",
        text: transfer.error || "Failed",
        title: transfer.error || "",
      });
    }

    if (transfer.outcome === "skipped") {
      return el(
        "div",
        { class: "transfer-status is-skipped", title: "Already here, so it was not sent again" },
        icon("skip"),
        el("span", { text: "Already had it" }),
      );
    }

    const show = el("button", { class: "link", type: "button" }, "Show");
    on(show, "click", () => {
      if (transfer.stored_path) void api.revealFile(transfer.stored_path).catch(() => {});
    });
    return el("div", { class: "transfer-status is-stored" }, icon("check"), show);
  }

  // The first paint comes from a direct call, so a window opened mid-transfer
  // is not blank until the next event arrives.
  void api
    .transfers()
    .then(render)
    .catch((err) => mount(list, el("p", { class: "error", text: message(err) })));

  const off = events<Snapshot>("transfers", render);

  // Timestamps in the recent list go stale on their own.
  const refresher = window.setInterval(() => {
    if (latest && (latest.active ?? []).length === 0) render(latest);
  }, 30_000);

  return {
    element,
    destroy: () => {
      off();
      window.clearInterval(refresher);
    },
  };
}

function bar(value: number): HTMLElement {
  const fill = el("div", { class: "bar-fill" });
  fill.style.width = `${value}%`;
  return el(
    "div",
    {
      class: "bar",
      role: "progressbar",
      "aria-valuenow": Math.round(value),
      "aria-valuemin": 0,
      "aria-valuemax": 100,
    },
    fill,
  );
}

// empty is the screen somebody sees most often: nothing is happening.
//
// It says what to do next rather than only reporting the absence of files,
// because "no transfers yet" on its own leaves a first-time user stuck.
function empty(status: Status, openPairing: () => void): HTMLElement {
  if (status.paired_devices === 0) {
    const button = el("button", { class: "button button-primary", type: "button" }, "Pair a phone");
    on(button, "click", openPairing);
    return el(
      "div",
      { class: "empty" },
      el("h2", { text: "No phones paired yet" }),
      el("p", { text: "Pair one and its photos will appear here as they arrive." }),
      button,
    );
  }

  const folder = el("button", { class: "link", type: "button" }, status.dest);
  on(folder, "click", () => void api.openDestination().catch(() => {}));

  return el(
    "div",
    { class: "empty" },
    el("h2", { text: "Nothing arriving right now" }),
    el("p", {}, "Send something from your phone and it will show up here."),
    el("p", { class: "muted" }, "Files are saved to ", folder),
  );
}

// lastSeen is exported for the devices view, which shows the same phrasing.
export function lastSeen(iso: string | undefined): string {
  return iso ? when(iso) : "never";
}
