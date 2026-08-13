// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// What the window shows a person.
//
// P6's gate is that somebody who has never seen the app can pair and transfer
// without instructions. The Go-level gate proves the receiver works; these
// prove the window says what happened and what to do next -- which is the part
// a person actually reads.

import { beforeEach, describe, expect, it, vi } from "vitest";

import type { HistoryEntry, Snapshot, Transfer } from "../bridge";
import {
  emptySnapshot,
  install,
  queuedFile,
  sampleDevice,
  sampleOutput,
  sampleSettings,
  sampleStatus,
  settle,
  text,
} from "./harness";
import { devicesView } from "../views/devices";
import { historyView } from "../views/history";
import { settingsView } from "../views/settings";
import { transfersView } from "../views/transfers";
import { notRunningView, welcomeView } from "../views/welcome";

// jsdom does not implement <dialog>'s modal methods.
beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn();
  HTMLDialogElement.prototype.close = vi.fn();
  document.body.replaceChildren();
});

function transfer(over: Partial<Transfer> = {}): Transfer {
  return {
    upload_id: "u1",
    device_id: "d1",
    device_name: "An's iPhone",
    direction: "inbound",
    name: "IMG_4021.HEIC",
    kind: "photo",
    size: 4_000_000,
    offset: 4_000_000,
    started_at: new Date().toISOString(),
    outcome: "stored",
    stored_path: "An's iPhone/IMG_4021.HEIC",
    ...over,
  };
}

describe("the first screen", () => {
  it("shows a scannable code, what to do with it, and where files will go", async () => {
    install();
    const view = welcomeView(sampleStatus, () => {});
    document.body.append(view.element);
    await settle();

    // The code itself, as an image the camera can read.
    expect(view.element.querySelector(".qr svg")).not.toBeNull();

    const body = text(view.element);
    expect(body).toContain("Install Geda Transfer");
    expect(body).toContain("Point the phone at this code");

    // "Where did it go?" is the other half of a first-timer's question, and it
    // is answered before they have sent anything.
    expect(body).toContain(sampleStatus.dest);

    // The fingerprint is what they compare against the phone.
    expect(body).toContain(sampleStatus.fingerprint);

    // A code that cannot be scanned still has a way in.
    expect(text(view.element)).toContain("Can’t scan it?");
    expect(view.element.querySelector(".uri")?.textContent).toBeTruthy();

    view.destroy();
  });

  it("does not strand somebody who cannot pair right now", async () => {
    const stub = install();
    const done = vi.fn();
    const view = welcomeView(sampleStatus, done);
    document.body.append(view.element);
    await settle();

    const skip = [...view.element.querySelectorAll("button")].find(
      (b) => b.textContent === "I’ll do this later",
    );
    expect(skip).toBeDefined();

    skip!.click();
    await settle();

    expect(stub.go.FinishOnboarding).toHaveBeenCalled();
    expect(done).toHaveBeenCalled();
    view.destroy();
  });

  // A code left live is a credential the user believes they have put away.
  it("withdraws the pairing code when it is closed", async () => {
    const stub = install();
    const view = welcomeView(sampleStatus, () => {});
    document.body.append(view.element);
    await settle();

    view.destroy();
    await settle();

    expect(stub.go.CancelPairing).toHaveBeenCalled();
  });
});

describe("the transfers screen", () => {
  it("tells somebody with no phone paired what to do", async () => {
    install();
    const openPairing = vi.fn();
    const view = transfersView({ ...sampleStatus, paired_devices: 0 }, openPairing);
    document.body.append(view.element);
    await settle();

    expect(text(view.element)).toContain("No phones paired yet");

    const button = [...view.element.querySelectorAll("button")].find(
      (b) => b.textContent === "Pair a phone",
    );
    expect(button).toBeDefined();
    button!.click();
    expect(openPairing).toHaveBeenCalled();

    view.destroy();
  });

  it("says where files land when a phone is paired but nothing is arriving", async () => {
    install();
    const view = transfersView(sampleStatus, () => {});
    document.body.append(view.element);
    await settle();

    expect(text(view.element)).toContain("Nothing arriving right now");
    expect(text(view.element)).toContain(sampleStatus.dest);

    view.destroy();
  });

  it("shows what is arriving, with a rate and a total", async () => {
    const snapshot: Snapshot = {
      ...emptySnapshot(),
      active: [transfer({ outcome: "", offset: 2_000_000 })],
      bytes_per_second: 7_200_000,
      active_bytes: 2_000_000,
      active_total: 4_000_000,
    };
    const stub = install({ Transfers: vi.fn(async () => snapshot) });

    const view = transfersView(sampleStatus, () => {});
    document.body.append(view.element);
    await settle();

    const body = text(view.element);
    expect(body).toContain("Receiving 1 file");
    expect(body).toContain("7.2 MB/s");
    expect(body).toContain("2.0 MB of 4.0 MB");
    expect(body).toContain("IMG_4021.HEIC");
    expect(body).toContain("50%");
    expect(stub.go.Transfers).toHaveBeenCalled();

    view.destroy();
  });

  it("repaints when the receiver pushes a new snapshot", async () => {
    const stub = install();
    const view = transfersView(sampleStatus, () => {});
    document.body.append(view.element);
    await settle();

    expect(text(view.element)).not.toContain("VID_0042.MOV");

    stub.emit("transfers", {
      ...emptySnapshot(),
      recent: [transfer({ name: "VID_0042.MOV", kind: "video" })],
    });

    expect(text(view.element)).toContain("VID_0042.MOV");
    view.destroy();
  });

  // A file the receiver already had is neither a success nor a failure, and a
  // user who is told "done" for a file that was never written will not look
  // for it again.
  it("distinguishes stored, already-had, and failed", async () => {
    const stub = install({
      Transfers: vi.fn(async () => ({
        ...emptySnapshot(),
        recent: [
          transfer({ upload_id: "a", name: "A.HEIC", outcome: "stored" }),
          transfer({ upload_id: "b", name: "B.HEIC", outcome: "skipped" }),
          transfer({
            upload_id: "c",
            name: "C.HEIC",
            outcome: "failed",
            error: "content does not match the declared hash",
          }),
        ],
      })),
    });

    const view = transfersView(sampleStatus, () => {});
    document.body.append(view.element);
    await settle();

    const body = text(view.element);
    expect(body).toContain("Already had it");
    expect(body).toContain("content does not match the declared hash");

    // "Show" is offered for the one that is actually on disk.
    const show = [...view.element.querySelectorAll("button")].find(
      (b) => b.textContent === "Show",
    );
    expect(show).toBeDefined();
    show!.click();
    await settle();
    expect(stub.go.RevealFile).toHaveBeenCalledWith("An's iPhone/IMG_4021.HEIC");

    view.destroy();
  });

  // Filenames and device names come off a phone and are untrusted
  // (docs/PROTOCOL.md). The window must render them, never execute them.
  it("renders a hostile filename as text", async () => {
    const hostile = '<img src=x onerror="globalThis.__pwned = true">';
    install({
      Transfers: vi.fn(async () => ({
        ...emptySnapshot(),
        recent: [transfer({ name: hostile, device_name: hostile })],
      })),
    });

    const view = transfersView(sampleStatus, () => {});
    document.body.append(view.element);
    await settle();

    expect(view.element.querySelector("img")).toBeNull();
    expect((globalThis as Record<string, unknown>).__pwned).toBeUndefined();
    expect(view.element.querySelector(".transfer-name")?.textContent).toBe(hostile);

    view.destroy();
  });
});

describe("the settings screen", () => {
  it("shows what a filename will look like before anything is saved", async () => {
    install();
    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    expect(view.element.querySelector(".preview")?.textContent).toBe(
      "An's iPhone/2026/IMG_4021.HEIC",
    );
    expect(text(view.element)).toContain("{original_name}");
  });

  it("surfaces the reason a setting was refused, and keeps what was typed", async () => {
    const stub = install({
      SaveSettings: vi.fn(async () => {
        throw "unusable filename template: unknown variable {yyy}";
      }),
    });

    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const template = view.element.querySelectorAll<HTMLInputElement>(".input")[2]!;
    template.value = "{yyy}";

    const save = [...view.element.querySelectorAll("button")].find(
      (b) => b.textContent === "Save",
    )!;
    save.click();
    await settle();

    const error = view.element.querySelector(".error") as HTMLElement;
    expect(error.hidden).toBe(false);
    expect(error.textContent).toContain("unknown variable");

    // The value they typed is still there to correct.
    expect(template.value).toBe("{yyy}");
    // A refused save must not be reported as a success.
    expect((view.element.querySelector(".notice") as HTMLElement).hidden).toBe(true);
    expect(stub.go.SaveSettings).toHaveBeenCalled();
  });

  // An empty string from the picker means the user cancelled, which must not
  // wipe the destination they already had.
  it("leaves the destination alone when the folder picker is cancelled", async () => {
    install({ ChooseDestination: vi.fn(async () => "") });

    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const dest = view.element.querySelectorAll<HTMLInputElement>(".input")[1]!;
    const before = dest.value;

    const browse = [...view.element.querySelectorAll("button")].find(
      (b) => b.textContent === "Choose…",
    )!;
    browse.click();
    await settle();

    expect(dest.value).toBe(before);
  });

  it("keeps the dangerous settings behind Advanced", async () => {
    install();
    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const advanced = view.element.querySelector("details.settings-advanced");
    expect(advanced).not.toBeNull();
    expect((advanced as HTMLDetailsElement).open).toBe(false);
    expect(text(advanced!)).toContain("Port");
    expect(text(advanced!)).toContain("can stop phones finding this computer");
  });

  it("offers the output presets with the default chosen", async () => {
    install();
    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const radios = [...view.element.querySelectorAll<HTMLInputElement>('input[name="output-preset"]')];
    expect(radios.map((r) => r.value)).toEqual(["original", "compatible", "space-saving"]);
    expect(radios.find((r) => r.checked)?.value).toBe("original");

    // The destructive one has to say what it destroys, in its own hint
    // rather than behind a "learn more".
    expect(text(view.element)).toContain("The original is gone");
  });

  it("sends the chosen preset and no matrix", async () => {
    const stub = install();
    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const compatible = view.element.querySelector<HTMLInputElement>('input[value="compatible"]')!;
    compatible.checked = true;
    compatible.dispatchEvent(new Event("change"));

    [...view.element.querySelectorAll("button")].find((b) => b.textContent === "Save")!.click();
    await settle();

    const sent = vi.mocked(stub.go.SaveSettings).mock.calls[0]![0];
    expect(sent.output_preset).toBe("compatible");
    // A named preset must not also carry a table: core would then have two
    // answers for the same question.
    expect(sent.output_matrix).toBeNull();
  });

  // Touching the per-type table means the user wants something no preset
  // offers, so the preset has to follow the table rather than overrule it.
  it("switches to a custom policy when the per-type table is used", async () => {
    const stub = install();
    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const heic = view.element.querySelector<HTMLSelectElement>(".settings-matrix select")!;
    heic.value = "sidecar";
    heic.dispatchEvent(new Event("change"));

    expect(
      [...view.element.querySelectorAll<HTMLInputElement>('input[name="output-preset"]')].some(
        (r) => r.checked,
      ),
    ).toBe(false);

    [...view.element.querySelectorAll("button")].find((b) => b.textContent === "Save")!.click();
    await settle();

    const sent = vi.mocked(stub.go.SaveSettings).mock.calls[0]![0];
    expect(sent.output_preset).toBe("custom");
    expect(sent.output_matrix).toMatchObject({ heic: "sidecar", raw: "keep" });
  });

  // Half of the P8 gate, stated where a person could otherwise change it by
  // accident: there is no control on this screen that converts a negative.
  it("gives raw negatives no option but to be kept", async () => {
    install();
    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const selects = [...view.element.querySelectorAll<HTMLSelectElement>(".settings-matrix select")];
    const raw = selects[2]!;
    expect(raw.disabled).toBe(true);
    expect([...raw.options].map((o) => o.value)).toEqual(["keep"]);
  });

  it("says why nothing is being converted, and that the files are safe", async () => {
    install({
      Settings: vi.fn(async () => ({
        ...sampleSettings,
        output_preset: "compatible" as const,
        output: sampleOutput({
          effective: { heic: "sidecar", video: "sidecar", raw: "keep", other: "keep" },
          missing: { heic: "libheif (heif-convert) or ffmpeg", video: "ffmpeg" },
        }),
      })),
    });

    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const notice = view.element.querySelector(".output-notice") as HTMLElement;
    expect(notice.hidden).toBe(false);
    expect(notice.textContent).toContain("ffmpeg");
    expect(notice.textContent).toContain("stored exactly as they were sent");
    expect(notice.textContent).toContain("brew install");
  });

  // A receiver that converts nothing needs no converter, so a missing ffmpeg
  // is not worth mentioning on the default preset.
  it("says nothing about ffmpeg when nothing is being converted", async () => {
    install({
      Settings: vi.fn(async () => ({
        ...sampleSettings,
        output: sampleOutput({ missing: { heic: "ffmpeg", video: "ffmpeg" } }),
      })),
    });

    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    expect((view.element.querySelector(".output-notice") as HTMLElement).hidden).toBe(true);
  });

  // The whole point of the message: it has to arrive while the choice is
  // being made. Waiting for a save and a revisit is how somebody walks away
  // believing their photos are being converted when nothing is.
  it("warns as soon as a converting preset is picked, before any save", async () => {
    install({
      Settings: vi.fn(async () => ({
        ...sampleSettings,
        output: sampleOutput({ missing: { heic: "ffmpeg", video: "ffmpeg" } }),
      })),
    });

    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const notice = view.element.querySelector(".output-notice") as HTMLElement;
    expect(notice.hidden).toBe(true);

    const compatible = view.element.querySelector<HTMLInputElement>('input[value="compatible"]')!;
    compatible.checked = true;
    compatible.dispatchEvent(new Event("change"));

    expect(notice.hidden).toBe(false);
    expect(notice.textContent).toContain("ffmpeg is not installed");
  });

  // And it goes away again when the choice is undone, without a round trip.
  it("stops warning when the preset goes back to keeping originals", async () => {
    install({
      Settings: vi.fn(async () => ({
        ...sampleSettings,
        output_preset: "compatible" as const,
        output: sampleOutput({
          effective: { heic: "sidecar", video: "sidecar", raw: "keep", other: "keep" },
          missing: { heic: "ffmpeg", video: "ffmpeg" },
        }),
      })),
    });

    const view = settingsView(() => {});
    document.body.append(view.element);
    await settle();

    const notice = view.element.querySelector(".output-notice") as HTMLElement;
    expect(notice.hidden).toBe(false);

    const original = view.element.querySelector<HTMLInputElement>('input[value="original"]')!;
    original.checked = true;
    original.dispatchEvent(new Event("change"));

    expect(notice.hidden).toBe(true);
  });
});

describe("the history screen", () => {
  function entry(over: Partial<HistoryEntry> = {}): HistoryEntry {
    return {
      id: 1,
      device_id: "d1",
      device_name: "An's iPhone",
      name: "IMG_4021.HEIC",
      stored_path: "An's iPhone/IMG_4021.HEIC",
      kind: "photo",
      size: 4_000_000,
      hash: "abc",
      received_at: new Date().toISOString(),
      ...over,
    };
  }

  it("does not offer Show more when there is no more", async () => {
    install({ History: vi.fn(async () => [entry(), entry({ id: 2 })]) });

    const view = historyView();
    document.body.append(view.element);
    await settle();

    expect((view.element.querySelector(".more") as HTMLElement).hidden).toBe(true);
    expect(text(view.element)).toContain("IMG_4021.HEIC");
  });

  it("offers Show more when a full page came back", async () => {
    install({ History: vi.fn(async () => Array.from({ length: 100 }, (_, i) => entry({ id: i }))) });

    const view = historyView();
    document.body.append(view.element);
    await settle();

    expect((view.element.querySelector(".more") as HTMLElement).hidden).toBe(false);
  });

  it("says so when nothing has been received", async () => {
    install();
    const view = historyView();
    document.body.append(view.element);
    await settle();

    expect(text(view.element)).toContain("Nothing here yet");
  });
});

// A window that looks normal while quietly receiving nothing is the worst
// version of this failure.
describe("when the receiver is not running", () => {
  it("says so and offers the one screen that can fix it", () => {
    install();
    const openSettings = vi.fn();
    const element = notRunningView("listen on :47891: address already in use", openSettings);
    document.body.append(element);

    expect(text(element)).toContain("Not receiving");
    expect(text(element)).toContain("address already in use");

    const button = [...element.querySelectorAll("button")].find(
      (b) => b.textContent === "Open settings",
    )!;
    button.click();
    expect(openSettings).toHaveBeenCalled();
  });
});

// P7 is the direction where the wording matters most. This computer cannot
// push to a phone that is asleep, so a screen that says "Sent" the moment a
// file is chosen is lying about something the user will only discover hours
// later.
describe("sending to a phone", () => {
  it("offers to send to a paired device and says the phone has to come and get it", async () => {
    const stub = install({
      Devices: vi.fn(async () => [sampleDevice({ queued: 1 })]),
      Outbox: vi.fn(async () => [queuedFile({ state: "ready" })]),
    });

    const view = devicesView();
    await settle();

    const send = [...view.element.querySelectorAll("button")].find((b) =>
      text(b).includes("Send files"),
    );
    expect(send).toBeDefined();

    const queue = text(view.element);
    expect(queue).toContain("archive.zip");
    expect(queue).toContain("Waiting for the phone to open the app");
    // Never the word "sent" for something that has not moved.
    expect(queue).not.toContain("Sent");

    send?.click();
    await settle();
    expect(stub.go.ChooseAndSend).toHaveBeenCalledWith("phone-1");

    view.destroy();
  });

  it("says why a queued file failed rather than leaving it looking pending", async () => {
    install({
      Devices: vi.fn(async () => [sampleDevice()]),
      Outbox: vi.fn(async () => [
        queuedFile({ state: "failed", error: "the file is no longer there" }),
      ]),
    });

    const view = devicesView();
    await settle();

    expect(text(view.element)).toContain("the file is no longer there");
    view.destroy();
  });

  it("withdraws a queued file without touching the device", async () => {
    const stub = install({
      Devices: vi.fn(async () => [sampleDevice({ queued: 1 })]),
      Outbox: vi.fn(async () => [queuedFile()]),
    });

    const view = devicesView();
    await settle();

    const withdraw = [...view.element.querySelectorAll("button")].find(
      (b) => text(b) === "Withdraw",
    );
    withdraw?.click();
    await settle();

    expect(stub.go.CancelSend).toHaveBeenCalledWith("phone-1", "item-1");
    expect(stub.go.Unpair).not.toHaveBeenCalled();
    view.destroy();
  });

  it("does not offer to send to a device that has been removed", async () => {
    const stub = install({
      Devices: vi.fn(async () => [sampleDevice({ revoked: true })]),
    });

    const view = devicesView();
    await settle();

    const send = [...view.element.querySelectorAll("button")].find((b) =>
      text(b).includes("Send files"),
    );
    expect(send?.disabled).toBe(true);
    // Nothing can collect it, so nothing is asked for.
    expect(stub.go.Outbox).not.toHaveBeenCalled();
    view.destroy();
  });
});

describe("the transfers screen, both directions", () => {
  it("says which way each file is going", async () => {
    const outbound: Transfer = transfer({
      upload_id: "outbox:i1",
      direction: "outbound",
      name: "archive.zip",
      kind: "file",
      outcome: "",
      offset: 500_000,
      size: 2_000_000,
      stored_path: "",
    });

    install({
      Transfers: vi.fn(
        async (): Promise<Snapshot> => ({ ...emptySnapshot(), active: [outbound] }),
      ),
    });

    const view = transfersView(sampleStatus, () => {});
    await settle();

    const shown = text(view.element);
    expect(shown).toContain("Sending 1 file");
    expect(shown).toContain("to An's iPhone");
    expect(shown).not.toContain("from An's iPhone");

    view.destroy();
  });

  it("does not offer to reveal a file this computer sent, because it never left", async () => {
    const stub = install({
      Transfers: vi.fn(
        async (): Promise<Snapshot> => ({
          ...emptySnapshot(),
          recent: [
            transfer({ direction: "outbound", outcome: "stored", stored_path: "" }),
          ],
        }),
      ),
    });

    const view = transfersView(sampleStatus, () => {});
    await settle();

    const show = [...view.element.querySelectorAll("button")].find((b) => text(b) === "Show");
    expect(show).toBeUndefined();
    expect(text(view.element)).toContain("Sent");
    expect(stub.go.RevealFile).not.toHaveBeenCalled();

    view.destroy();
  });
});
