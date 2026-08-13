// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// The settings view.
//
// Everything above "Advanced" has a sensible default and can be left alone
// (AGENTS.md §4). Everything below it can stop phones finding this machine, so
// it is separated and each control says what it costs.

import type {
  FileClass,
  OutputAction,
  OutputPreset,
  OutputView,
  Settings,
  SettingsView,
} from "../bridge";
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
    const output = outputSection(view);

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
        output_preset: output.preset(),
        output_matrix: output.matrix(),
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
      output.element,
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
        "settings-advanced",
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

function details(
  className: string,
  title: string,
  warning: string,
  ...children: (Node | null)[]
): HTMLElement {
  return el(
    "details",
    { class: `settings-group ${className}` },
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

// The output section: what happens to files after they arrive.
//
// Three presets, and an advanced table that spells out what each one does per
// file type. The table is shown filled in whatever the preset -- picking
// "Space-saving" and reading what it will actually do to your videos should be
// one gesture, not two.
function outputSection(view: SettingsView): {
  element: HTMLElement;
  preset: () => OutputPreset;
  matrix: () => Partial<Record<FileClass, OutputAction>> | null;
} {
  const out = view.output;
  let chosen: OutputPreset = view.output_preset;
  let table: Record<string, OutputAction> = { ...out.effective };

  const notice = el("p", { class: "output-notice", hidden: true });
  const rows = el("div", { class: "matrix" });

  // The advanced table drives the preset, not the other way round: touching
  // any row means the user wants something no preset offers, which is exactly
  // what "custom" is.
  function chooseCustom(): void {
    chosen = "custom";
    for (const input of radios) input.checked = false;
    renderNotice();
  }

  function renderRows(): void {
    mount(
      rows,
      ...out.classes.map((cls) => {
        const fixed = cls === "raw";
        const select = el(
          "select",
          { class: "select", disabled: fixed },
          ...out.actions
            .filter((action) => !fixed || action === "keep")
            .map((action) =>
              el("option", { value: action, selected: table[cls] === action, text: actionLabel(action) }),
            ),
        );
        on(select, "change", () => {
          table = { ...table, [cls]: select.value as OutputAction };
          chooseCustom();
        });
        return el(
          "label",
          { class: "field matrix-row" },
          el("span", { class: "field-label", text: classLabel(cls) }),
          select,
          el("span", { class: "field-hint", text: classHint(cls) }),
        );
      }),
    );
  }

  function renderNotice(): void {
    // Recomputed for whatever is selected, not for what was last saved. The
    // Go side's `unavailable` describes the saved policy, so on a machine
    // with no ffmpeg and the default preset it is empty -- and showing it as
    // it stands would leave somebody picking a converting preset with no
    // warning at all until they saved and came back.
    const missing = missingFor(out, table);

    // Two different things can be worth saying, and only one of them is a
    // problem. A missing converter matters; a queue that is still working is
    // just progress.
    const parts: string[] = [];
    if (missing) parts.push(missing);
    if (out.pending > 0) {
      parts.push(`${out.pending} file${out.pending === 1 ? "" : "s"} still being converted.`);
    }
    notice.textContent = parts.join(" ");
    notice.hidden = parts.length === 0;
    notice.classList.toggle("is-bad", Boolean(missing));
  }

  const choices = out.presets.map((preset) => {
    const input = el("input", {
      type: "radio",
      name: "output-preset",
      value: preset,
      checked: preset === chosen,
    });
    on(input, "change", () => {
      chosen = preset;
      table = presetTable(out, preset);
      renderRows();
      renderNotice();
    });
    return { input, row: toggle(input, presetLabel(preset), presetHint(preset)) };
  });
  const radios = choices.map((choice) => choice.input);

  renderRows();
  renderNotice();

  const element = group(
    "What arrives",
    ...choices.map((choice) => choice.row),
    notice,
    details(
      "settings-matrix",
      "Per file type",
      "Choosing anything here means none of the presets above; the receiver follows this table instead.",
      rows,
      el("p", {
        class: "muted",
        text:
          "Raw negatives are always kept exactly as they arrived. There is no conversion of a " +
          "raw file that is not a downgrade, and the original cannot be recovered from one.",
      }),
    ),
  );

  return {
    element,
    preset: () => chosen,
    matrix: () => (chosen === "custom" ? (table as Partial<Record<FileClass, OutputAction>>) : null),
  };
}

/**
 * What is missing, for the actions currently selected.
 *
 * Only for classes this table actually converts: a receiver keeping originals
 * needs no converter, and telling that person to install ffmpeg is noise about
 * a problem they do not have.
 */
function missingFor(out: OutputView, table: Record<string, OutputAction>): string {
  const needed = new Set<string>();
  for (const cls of out.classes) {
    if ((table[cls] ?? "keep") === "keep") continue;
    const tool = out.missing?.[cls];
    if (tool) needed.add(tool);
  }
  if (needed.size === 0) return "";

  // The files are safe, and that has to be in the same sentence as the
  // problem: an alarming line about a missing dependency reads like data loss
  // when the actual outcome is that originals are kept.
  return (
    `${[...needed].join(" and ")} is not installed, so nothing will be converted. ` +
    `Files still arrive and are stored exactly as they were sent. ${out.install}`
  );
}

// presetTable is what a named preset resolves to, kept in step with
// core/formats. It is only used to fill the advanced table in; the receiver
// takes the preset name and applies its own copy.
function presetTable(out: OutputView, preset: OutputPreset): Record<string, OutputAction> {
  const of = (media: OutputAction): Record<string, OutputAction> => ({
    heic: media,
    video: media,
    raw: "keep",
    other: "keep",
  });
  switch (preset) {
    case "compatible":
      return of("sidecar");
    case "space-saving":
      return of("replace");
    case "original":
      return of("keep");
    default:
      return { ...out.effective };
  }
}

function presetLabel(preset: OutputPreset): string {
  switch (preset) {
    case "original":
      return "Keep originals";
    case "compatible":
      return "Also save a copy anything can open";
    case "space-saving":
      return "Convert and delete the original";
    default:
      return "Per file type";
  }
}

function presetHint(preset: OutputPreset): string {
  switch (preset) {
    case "original":
      return "Nothing is converted. A HEIC stays a HEIC. This is the default.";
    case "compatible":
      return "Photos also get a JPEG and videos an H.264 copy, beside the original. Uses more disk.";
    case "space-saving":
      return "The converted copy replaces the original, once it has been written. The original is gone.";
    default:
      return "";
  }
}

function classLabel(cls: FileClass): string {
  switch (cls) {
    case "heic":
      return "iPhone photos (HEIC)";
    case "video":
      return "Videos";
    case "raw":
      return "Raw negatives (ProRAW, DNG)";
    default:
      return "Everything else";
  }
}

function classHint(cls: FileClass): string {
  switch (cls) {
    case "heic":
      return "Converted to JPEG.";
    case "video":
      return "Converted to H.264 in MP4. A video already in H.264 is left alone.";
    case "raw":
      return "Always kept.";
    default:
      return "JPEG, PNG, and anything sent as a file are never converted.";
  }
}

function actionLabel(action: OutputAction): string {
  switch (action) {
    case "keep":
      return "Keep as it arrived";
    case "sidecar":
      return "Keep both";
    default:
      return "Replace the original";
  }
}
