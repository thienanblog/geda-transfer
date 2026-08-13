// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The devices view: what has paired, and how to add or remove one.

import type { Device, QueuedFile } from "../bridge";
import { api, message } from "../api";
import { ago, bytes, plural } from "../format";
import { el, icon, mount, on } from "../dom";
import { outboxSection } from "./outbox";
import { pairingDialog } from "./pairing";

export function devicesView(): { element: HTMLElement; destroy: () => void } {
  const list = el("div", { class: "device-list" });
  const add = el(
    "button",
    { class: "button button-primary", type: "button" },
    icon("plus"),
    "Pair a phone",
  );

  const element = el(
    "section",
    { class: "view" },
    el(
      "div",
      { class: "view-header" },
      el("h1", { text: "Devices" }),
      add,
    ),
    list,
  );

  on(add, "click", () => pairingDialog(() => void load()));

  // Queues are held here rather than fetched by each card, so that the ten
  // second refresh redraws in one pass instead of leaving every panel to
  // arrive on its own and flicker.
  let queues = new Map<string, QueuedFile[]>();

  async function load(): Promise<void> {
    try {
      const devices = await api.devices();
      queues = await loadQueues(devices);
      render(devices);
    } catch (err) {
      mount(list, el("p", { class: "error", text: message(err) }));
    }
  }

  async function loadQueues(devices: Device[]): Promise<Map<string, QueuedFile[]>> {
    const active = devices.filter((device) => !device.revoked);
    const loaded = await Promise.all(
      // A queue that cannot be read is not worth failing the whole screen
      // over: the device list is still the useful part of it.
      active.map((device) => api.outbox(device.id).catch((): QueuedFile[] => [])),
    );
    return new Map(active.map((device, i) => [device.id, loaded[i] ?? []]));
  }

  function render(devices: Device[]): void {
    if (devices.length === 0) {
      const button = el("button", { class: "button button-primary", type: "button" }, "Pair a phone");
      on(button, "click", () => pairingDialog(() => void load()));
      mount(
        list,
        el(
          "div",
          { class: "empty" },
          el("h2", { text: "No phones paired yet" }),
          el("p", {
            text: "Pairing shows a code on this screen for the phone's camera. It takes a few seconds and only has to be done once per phone.",
          }),
          button,
        ),
      );
      return;
    }

    mount(list, ...devices.map(card));
  }

  function card(device: Device): HTMLElement {
    const remove = el(
      "button",
      { class: "button button-quiet", type: "button" },
      device.revoked ? "Removed" : "Remove",
    );
    if (device.revoked) remove.disabled = true;

    on(remove, "click", () => void confirmRemove(device));

    const send = el("button", { class: "button", type: "button" }, icon("send"), "Send files");
    if (device.revoked) send.disabled = true;

    // Only devices that can still collect get a queue: a removed phone has
    // none, and offering to send to one would be offering something that
    // cannot happen.
    const queue = device.revoked
      ? undefined
      : outboxSection(device, queues.get(device.id) ?? [], {
          withdraw: (item) => void withdraw(device, item),
          clearFinished: () => void clearFinished(device),
        });

    on(send, "click", () => void chooseAndSend(device, send));

    const meta = device.revoked
      ? "Removed — its files are still here"
      : `Last seen ${ago(device.last_seen_at)} · ${plural(device.files, "file")} · ${bytes(device.bytes)}`;

    return el(
      "div",
      { class: `card card-device ${device.revoked ? "is-revoked" : ""}` },
      el(
        "div",
        { class: "card-row" },
        el("div", { class: "card-icon" }, icon("devices")),
        el(
          "div",
          { class: "card-main" },
          el("div", { class: "card-title", text: device.name }),
          el("div", { class: "card-meta", text: meta }),
        ),
        send,
        remove,
      ),
      queue,
    );
  }

  // Sending is queueing. This computer cannot push to a phone that is asleep
  // (AGENTS.md §3.7), so the confirmation says what will actually happen
  // rather than reporting a transfer that has not started.
  async function chooseAndSend(device: Device, button: HTMLButtonElement): Promise<void> {
    button.disabled = true;
    try {
      const result = await api.chooseAndSend(device.id);
      if (result.cancelled) return;
      await load();
    } catch (err) {
      mount(list, el("p", { class: "error", text: message(err) }));
    } finally {
      button.disabled = false;
    }
  }

  async function withdraw(device: Device, item: QueuedFile): Promise<void> {
    try {
      await api.cancelSend(device.id, item.id);
    } catch (err) {
      mount(list, el("p", { class: "error", text: message(err) }));
      return;
    }
    await load();
  }

  async function clearFinished(device: Device): Promise<void> {
    try {
      await api.clearSent(device.id);
    } catch (err) {
      mount(list, el("p", { class: "error", text: message(err) }));
      return;
    }
    await load();
  }

  // Removing a device is destructive to a credential, not to files, and the
  // dialog says exactly that. A user who thinks "Remove" might delete their
  // photos will never press it.
  async function confirmRemove(device: Device): Promise<void> {
    const confirmed = await confirmDialog({
      title: `Remove ${device.name}?`,
      body: "It will not be able to send anything until it is paired again. The files it already sent stay where they are.",
      confirm: "Remove",
    });
    if (!confirmed) return;

    try {
      await api.unpair(device.id);
      await load();
    } catch (err) {
      mount(list, el("p", { class: "error", text: message(err) }));
    }
  }

  void load();
  const refresher = window.setInterval(() => void load(), 10_000);

  return { element, destroy: () => window.clearInterval(refresher) };
}

interface ConfirmOptions {
  title: string;
  body: string;
  confirm: string;
}

export function confirmDialog(options: ConfirmOptions): Promise<boolean> {
  return new Promise((resolve) => {
    const dialog = el("dialog", { class: "dialog" });
    const cancel = el("button", { class: "button", type: "button" }, "Cancel");
    const confirm = el("button", { class: "button button-danger", type: "button" }, options.confirm);

    const finish = (result: boolean): void => {
      dialog.close();
      dialog.remove();
      resolve(result);
    };

    mount(
      dialog,
      el(
        "div",
        { class: "dialog-body" },
        el("h2", { text: options.title }),
        el("p", { text: options.body }),
        el("div", { class: "dialog-actions" }, cancel, confirm),
      ),
    );

    on(cancel, "click", () => finish(false));
    on(confirm, "click", () => finish(true));
    dialog.addEventListener("cancel", (e) => {
      e.preventDefault();
      finish(false);
    });

    document.body.append(dialog);
    dialog.showModal();
  });
}
