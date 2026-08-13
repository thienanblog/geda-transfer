// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The pairing panel: a QR code and the three sentences that go with it.

import type { PairCode } from "../bridge";
import { api, message } from "../api";
import { countdown } from "../format";
import { clear, el, mount, on } from "../dom";

export interface PairingOptions {
  // onPaired fires when a new device appears while the panel is open, so the
  // window can move on by itself. A user who has just scanned a code should
  // not also have to work out what to click next.
  onPaired?: () => void;

  // compact drops the numbered steps, for the panel shown to somebody who has
  // already paired a phone once and does not need telling again.
  compact?: boolean;
}

export function pairingPanel(options: PairingOptions = {}): {
  element: HTMLElement;
  destroy: () => void;
} {
  const code = el("div", { class: "qr" });
  const status = el("p", { class: "muted" });
  const fingerprint = el("code", { class: "fingerprint" });
  const uri = el("code", { class: "uri" });
  const error = el("p", { class: "error", hidden: true });

  const refresh = el("button", { class: "button", type: "button" }, "Show a new code");

  let expires = "";
  let devicesAtStart = -1;
  let stopped = false;

  const element = el(
    "div",
    { class: "pairing" },
    el(
      "div",
      { class: "pairing-code" },
      code,
      status,
      el("div", { class: "pairing-identity" }, el("span", { text: "Fingerprint" }), fingerprint),
    ),
    el(
      "div",
      { class: "pairing-help" },
      options.compact
        ? el("p", {
            text: "Open Geda Transfer on the phone and scan this code.",
          })
        : el(
            "ol",
            { class: "steps" },
            el("li", {}, el("strong", { text: "Install Geda Transfer" }), " on your phone."),
            el("li", {}, "Open it and tap ", el("strong", { text: "Add a receiver" }), "."),
            el("li", {}, "Point the phone at this code."),
          ),
      el(
        "details",
        { class: "manual" },
        el("summary", { text: "Can’t scan it?" }),
        el("p", {
          text: "Type this into the phone instead. It works the same way and expires just as fast.",
        }),
        uri,
      ),
      error,
      refresh,
    ),
  );

  async function load(): Promise<void> {
    error.hidden = true;
    try {
      const [pairCode, devices] = await Promise.all([api.pair(), api.devices()]);
      devicesAtStart = devices.filter((d) => !d.revoked).length;
      show(pairCode);
    } catch (err) {
      showError(message(err));
    }
  }

  function show(pairCode: PairCode): void {
    expires = pairCode.expires_at;
    // The SVG is produced by this app's own Go code from the pairing URI, so
    // it is the one place markup is inserted from a value rather than a
    // literal. It contains no text from any device.
    clear(code);
    code.innerHTML = pairCode.svg;

    fingerprint.textContent = pairCode.fingerprint;
    uri.textContent = pairCode.uri;
    tick();
  }

  function showError(text: string): void {
    clear(code);
    code.append(el("div", { class: "qr-empty", text: "No code" }));
    error.textContent = text;
    error.hidden = false;
    status.textContent = "";
  }

  function tick(): void {
    if (!expires) return;
    const left = countdown(expires);
    if (left === "0:00") {
      status.textContent = "This code has expired.";
      return;
    }
    status.textContent = `Expires in ${left} · single use`;
  }

  // Polling for a new device is the only way the window can know: pairing
  // happens over TLS between the phone and the receiver, and nothing about it
  // passes through the page.
  async function poll(): Promise<void> {
    if (stopped || devicesAtStart < 0) return;
    try {
      const devices = await api.devices();
      const now = devices.filter((d) => !d.revoked).length;
      if (now > devicesAtStart) {
        devicesAtStart = now;
        options.onPaired?.();
      }
    } catch {
      // A failed poll is not worth showing: the next one is a second away.
    }
  }

  const ticker = window.setInterval(tick, 1000);
  const poller = window.setInterval(() => void poll(), 1500);

  on(refresh, "click", () => void load());
  void load();

  return {
    element,
    destroy: () => {
      stopped = true;
      window.clearInterval(ticker);
      window.clearInterval(poller);
      // A code left live is a credential the user believes they have put away.
      void api.cancelPairing().catch(() => {});
    },
  };
}

// pairingDialog wraps the panel in a modal.
export function pairingDialog(onPaired: () => void): void {
  const panel = pairingPanel({
    compact: true,
    onPaired: () => {
      close();
      onPaired();
    },
  });

  const dialog = el("dialog", { class: "dialog" });
  const done = el("button", { class: "button", type: "button" }, "Done");

  mount(
    dialog,
    el(
      "div",
      { class: "dialog-body" },
      el("h2", { text: "Pair a phone" }),
      panel.element,
      el("div", { class: "dialog-actions" }, done),
    ),
  );

  function close(): void {
    panel.destroy();
    dialog.close();
    dialog.remove();
  }

  on(done, "click", close);
  dialog.addEventListener("cancel", (e) => {
    e.preventDefault();
    close();
  });

  document.body.append(dialog);
  dialog.showModal();
}
