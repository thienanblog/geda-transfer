// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The history view: everything this machine has received.

import type { Device, HistoryEntry } from "../bridge";
import { api, HISTORY_PAGE, message } from "../api";
import { bytes, when } from "../format";
import { el, icon, mount, on } from "../dom";

export function historyView(): { element: HTMLElement; destroy: () => void } {
  const rows = el("div", { class: "history-list" });
  const filter = el("select", { class: "select" });
  const more = el("button", { class: "button", type: "button" }, "Show more");
  const moreRow = el("div", { class: "more" }, more);
  moreRow.hidden = true;

  const element = el(
    "section",
    { class: "view" },
    el(
      "div",
      { class: "view-header" },
      el("h1", { text: "History" }),
      el("label", { class: "field-inline" }, el("span", { text: "Device" }), filter),
    ),
    rows,
    moreRow,
  );

  let entries: HistoryEntry[] = [];

  async function loadDevices(): Promise<void> {
    try {
      const devices: Device[] = await api.devices();
      mount(
        filter,
        el("option", { value: "", text: "All devices" }),
        ...devices.map((d) => el("option", { value: d.id, text: d.name })),
      );
    } catch {
      mount(filter, el("option", { value: "", text: "All devices" }));
    }
  }

  async function load(reset: boolean): Promise<void> {
    // Paging is by the timestamp of the last row rather than an offset, so a
    // file arriving mid-scroll cannot make a row appear twice or vanish.
    const before = reset ? "" : (entries.at(-1)?.received_at ?? "");
    try {
      const page = await api.history(filter.value, before, HISTORY_PAGE);
      entries = reset ? page : entries.concat(page);
      render();
      // A short page is the end of the list. Offering "Show more" until a
      // click returns nothing shows a control that does nothing.
      moreRow.hidden = page.length < HISTORY_PAGE;
    } catch (err) {
      mount(rows, el("p", { class: "error", text: message(err) }));
    }
  }

  function render(): void {
    if (entries.length === 0) {
      mount(
        rows,
        el(
          "div",
          { class: "empty" },
          el("h2", { text: "Nothing here yet" }),
          el("p", { text: "Files show up here once a phone has sent some." }),
        ),
      );
      return;
    }
    mount(rows, ...entries.map(row));
  }

  function row(entry: HistoryEntry): HTMLElement {
    const kindIcon = entry.kind === "photo" ? "photo" : entry.kind === "video" ? "video" : "file";
    const show = el("button", { class: "link", type: "button" }, "Show");
    on(show, "click", () => void api.revealFile(entry.stored_path).catch(() => {}));

    return el(
      "div",
      { class: "history-row" },
      el("div", { class: "transfer-icon" }, icon(kindIcon)),
      el(
        "div",
        { class: "transfer-main" },
        el("div", { class: "transfer-name", text: entry.name }),
        el("div", {
          class: "transfer-meta",
          text: `${entry.device_name} · ${bytes(entry.size)} · ${when(entry.received_at)}`,
        }),
      ),
      show,
    );
  }

  on(filter, "change", () => void load(true));
  on(more, "click", () => void load(false));

  void loadDevices().then(() => load(true));

  return { element, destroy: () => {} };
}
