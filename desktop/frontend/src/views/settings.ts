// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The settings view.
//
// Everything above "Advanced" has a sensible default and can be left alone
// (AGENTS.md §4). Everything below it can stop phones finding this machine, so
// it is separated and each control says what it costs.

import type { Settings, SettingsView } from "../bridge";
import { api, message } from "../api";
import { el, mount, on } from "../dom";

export function settingsView(onSaved: () => void): { element: HTMLElement; destroy: () => void } {
  const body = el("div", { class: "settings" });
  const element = el(
    "section",
    { class: "view" },
    el("div", { class: "view-header" }, el("h1", { text: "Settings" })),
    body,
  );

  let current: SettingsView | null = null;

  async function load(): Promise<void> {
    try {
      current = await api.settings();
      render(current);
    } catch (err) {
      mount(body, el("p", { class: "error", text: message(err) }));
    }
  }

  function render(view: SettingsView): void {
    const name = textInput(view.name);
    const dest = textInput(view.dest);
    const template = textInput(view.template);
    const port = numberInput(view.port);
    const advertise = textInput((view.advertise ?? []).join(", "));
    const mdns = checkbox(view.mdns);
    const discovery = checkbox(view.discovery);
    const autostart = checkbox(view.autostart);

    const preview = el("code", { class: "preview", text: view.template_preview });
    const notice = el("p", { class: "notice", hidden: true });
    const error = el("p", { class: "error", hidden: true });
    const save = el("button", { class: "button button-primary", type: "button" }, "Save");

    const browse = el("button", { class: "button", type: "button" }, "Choose…");
    on(browse, "click", () => {
      void api
        .chooseDestination()
        .then((chosen) => {
          // An empty string means the picker was cancelled, which is not a
          // failure and must not change anything.
          if (chosen) dest.value = chosen;
        })
        .catch((err) => showError(message(err)));
    });

    const reset = el("button", { class: "link", type: "button" }, "Use the default");
    on(reset, "click", () => {
      template.value = view.default_template;
      void updatePreview();
    });

    async function updatePreview(): Promise<void> {
      try {
        preview.textContent = await api.previewTemplate(template.value);
        preview.classList.remove("is-bad");
      } catch (err) {
        preview.textContent = message(err);
        preview.classList.add("is-bad");
      }
    }
    on(template, "input", () => void updatePreview());

    function showError(text: string): void {
      error.textContent = text;
      error.hidden = false;
      notice.hidden = true;
    }

    on(save, "click", () => {
      const next: Settings = {
        name: name.value,
        dest: dest.value,
        port: Number(port.value),
        advertise: advertise.value
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        mdns: mdns.checked,
        discovery: discovery.checked,
        autostart: autostart.checked,
        onboarded: view.onboarded,
        template: template.value,
      };

      save.disabled = true;
      void api
        .saveSettings(next)
        .then((saved) => {
          current = saved;
          error.hidden = true;
          notice.textContent = "Saved.";
          notice.hidden = false;
          onSaved();
        })
        .catch((err) => showError(message(err)))
        .finally(() => {
          save.disabled = false;
        });
    });

    mount(
      body,
      group(
        "This computer",
        field(
          "Name",
          name,
          "What phones see when choosing where to send.",
        ),
        field(
          "Save files to",
          el("div", { class: "field-row" }, dest, browse),
          "Received photos, videos, and files are written here.",
        ),
        view.autostart_supported
          ? toggle(
              autostart,
              "Start when I log in",
              "A phone cannot wake this computer, so the app has to be running for anything to arrive.",
            )
          : null,
      ),
      group(
        "File names",
        field(
          "Template",
          el("div", { class: "field-row" }, template, reset),
          "",
        ),
        el(
          "div",
          { class: "field-help" },
          el("div", {}, el("span", { text: "Example: " }), preview),
          el("div", {
            class: "muted",
            text: `Variables: ${view.template_variables.map((v) => `{${v}}`).join(" ")}`,
          }),
        ),
      ),
      details(
        "Advanced",
        "These are safe to leave alone. Changing them can stop phones finding this computer.",
        field(
          "Port",
          port,
          "Phones remember this. Changing it means pairing each phone again.",
        ),
        field(
          "Addresses to advertise",
          advertise,
          "Leave empty to use every address this computer has, including VPN ones. Set it only if this machine is behind a port mapping.",
        ),
        toggle(mdns, "Announce over Bonjour", "Finds phones on the same network. Harmless to leave on."),
        toggle(
          discovery,
          "Answer discovery probes",
          "Turning this off leaves only pairing codes and addresses phones already know.",
        ),
      ),
      el("div", { class: "settings-actions" }, save, notice, error),
    );
  }

  void load();
  return { element, destroy: () => {} };
}

function group(title: string, ...children: (Node | null)[]): HTMLElement {
  return el(
    "div",
    { class: "settings-group" },
    el("h2", { class: "group-title", text: title }),
    ...children.filter((c): c is Node => c !== null),
  );
}

function details(title: string, warning: string, ...children: (Node | null)[]): HTMLElement {
  return el(
    "details",
    { class: "settings-group settings-advanced" },
    el("summary", { text: title }),
    el("p", { class: "muted", text: warning }),
    ...children.filter((c): c is Node => c !== null),
  );
}

function field(label: string, control: Node, help: string): HTMLElement {
  return el(
    "label",
    { class: "field" },
    el("span", { class: "field-label", text: label }),
    control,
    help ? el("span", { class: "field-hint", text: help }) : null,
  );
}

function toggle(input: HTMLInputElement, label: string, help: string): HTMLElement {
  return el(
    "label",
    { class: "field field-toggle" },
    input,
    el(
      "span",
      {},
      el("span", { class: "field-label", text: label }),
      el("span", { class: "field-hint", text: help }),
    ),
  );
}

function textInput(value: string): HTMLInputElement {
  return el("input", { class: "input", type: "text", value });
}

function numberInput(value: number): HTMLInputElement {
  return el("input", { class: "input input-narrow", type: "number", value, min: 1, max: 65535 });
}

function checkbox(checked: boolean): HTMLInputElement {
  return el("input", { type: "checkbox", checked });
}
