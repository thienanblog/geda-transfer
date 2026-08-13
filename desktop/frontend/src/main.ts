// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The window: a sidebar, four screens, and the header that says whether this
// machine is actually receiving.

import "./style.css";

import type { ReceiverEvent, Status } from "./bridge";
import { api, events, message } from "./api";
import { el, icon, mount, on } from "./dom";
import { devicesView } from "./views/devices";
import { historyView } from "./views/history";
import { pairingDialog } from "./views/pairing";
import { settingsView } from "./views/settings";
import { transfersView } from "./views/transfers";
import { notRunningView, welcomeView } from "./views/welcome";

type Screen = "transfers" | "devices" | "history" | "settings";

interface View {
  element: HTMLElement;
  destroy: () => void;
}

const root = document.getElementById("app");
if (!root) throw new Error("no mount point");

let status: Status | null = null;
let screen: Screen = "transfers";
let active: View | null = null;

const content = el("main", { class: "content" });
const statusLine = el("div", { class: "app-status" });

const nav = el("nav", { class: "nav" });
const shell = el(
  "div",
  { class: "shell" },
  el(
    "aside",
    { class: "sidebar" },
    el("div", { class: "brand" }, el("span", { class: "brand-mark" }, icon("transfers")), "Geda Transfer"),
    nav,
    statusLine,
  ),
  content,
);

function navButton(target: Screen, label: string): HTMLElement {
  const button = el(
    "button",
    { class: `nav-item ${screen === target ? "is-current" : ""}`, type: "button" },
    icon(target),
    el("span", { text: label }),
  );
  on(button, "click", () => show(target));
  return button;
}

function renderNav(): void {
  mount(
    nav,
    navButton("transfers", "Transfers"),
    navButton("devices", "Devices"),
    navButton("history", "History"),
    navButton("settings", "Settings"),
  );
}

function renderStatus(): void {
  if (!status) return;

  if (!status.running) {
    mount(
      statusLine,
      el("span", { class: "dot dot-bad" }),
      el("span", { text: "Not receiving" }),
    );
    return;
  }

  const folder = el("button", { class: "link", type: "button" }, "Open folder");
  on(folder, "click", () => void api.openDestination().catch(() => {}));

  mount(
    statusLine,
    el(
      "div",
      { class: "app-status-line" },
      el("span", { class: "dot dot-ready" }),
      el("span", { class: "app-status-name", text: status.name }),
    ),
    el("div", { class: "app-status-meta", text: `${status.paired_devices} paired` }),
    folder,
  );
}

function show(target: Screen): void {
  screen = target;
  active?.destroy();
  active = null;
  renderNav();

  if (!status) return;

  if (!status.running && target !== "settings") {
    mount(content, notRunningView(status.error ?? "The receiver is not running.", () => show("settings")));
    return;
  }

  switch (target) {
    case "transfers":
      active = transfersView(status, () => pairingDialog(() => void refresh()));
      break;
    case "devices":
      active = devicesView();
      break;
    case "history":
      active = historyView();
      break;
    case "settings":
      active = settingsView(() => void refresh());
      break;
  }
  mount(content, active.element);
}

// refresh re-reads the status and repaints whatever depends on it.
async function refresh(): Promise<void> {
  try {
    status = await api.status();
  } catch (err) {
    mount(root!, el("div", { class: "fatal", text: message(err) }));
    return;
  }

  if (!status.onboarded) {
    showWelcome();
    return;
  }

  if (root!.firstChild !== shell) mount(root!, shell);
  renderStatus();
  show(screen);
}

function showWelcome(): void {
  active?.destroy();
  const view = welcomeView(status!, () => void refresh());
  active = view;
  mount(root!, view.element);
}

events<ReceiverEvent>("receiver", () => void refresh());

// The header's counters -- paired devices, whether it is running -- change
// without an event of their own, so they are re-read on a slow timer. The
// transfer list is pushed and does not depend on this.
window.setInterval(() => {
  if (screen === "transfers" && status?.onboarded) void refreshStatusOnly();
}, 5000);

async function refreshStatusOnly(): Promise<void> {
  try {
    const next = await api.status();
    const changed =
      !status ||
      next.running !== status.running ||
      next.paired_devices !== status.paired_devices ||
      next.name !== status.name;
    status = next;
    renderStatus();
    if (changed) show(screen);
  } catch {
    // The next tick will try again.
  }
}

void refresh();
