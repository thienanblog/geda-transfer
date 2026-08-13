// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The first thing anybody sees.
//
// The phase gate is that somebody who has never seen this app can pair and
// transfer without instructions (docs/PLAN.md P6). That is decided here: the
// first screen has one job, shows the code needed to do it, and says where the
// files will land -- because "where did it go?" is the other half of the
// question a first-time user has.

import type { Status } from "../bridge";
import { api } from "../api";
import { el, mount, on } from "../dom";
import { pairingPanel } from "./pairing";

export function welcomeView(status: Status, onDone: () => void): {
  element: HTMLElement;
  destroy: () => void;
} {
  const panel = pairingPanel({
    onPaired: () => {
      void api.finishOnboarding().finally(onDone);
    },
  });

  const folder = el("button", { class: "link", type: "button" }, status.dest);
  on(folder, "click", () => void api.openDestination().catch(() => {}));

  const skip = el("button", { class: "link", type: "button" }, "I’ll do this later");
  on(skip, "click", () => {
    void api.finishOnboarding().finally(onDone);
  });

  const element = el(
    "section",
    { class: "welcome" },
    el(
      "header",
      { class: "welcome-header" },
      el("h1", { text: "Send photos from your phone" }),
      el("p", {
        text: "Everything stays on your own network. No account, no cloud, nothing to sign up for.",
      }),
    ),
    panel.element,
    el(
      "footer",
      { class: "welcome-footer" },
      el("p", { class: "muted" }, "Files will be saved to ", folder),
      skip,
    ),
  );

  return { element, destroy: panel.destroy };
}

// notRunning is shown when the receiver could not start.
//
// It replaces the whole window on purpose: nothing else in the app works, and
// a window that looks normal while quietly receiving nothing is the worst
// version of this failure.
export function notRunningView(error: string, openSettings: () => void): HTMLElement {
  const settings = el("button", { class: "button button-primary", type: "button" }, "Open settings");
  on(settings, "click", openSettings);

  const element = el("section", { class: "view" });
  mount(
    element,
    el(
      "div",
      { class: "empty" },
      el("h2", { text: "Not receiving" }),
      el("p", { text: error }),
      el("p", {
        class: "muted",
        text: "The usual cause is another program already using the port, or a folder that is no longer there.",
      }),
      settings,
    ),
  );
  return element;
}
