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
import { emptySnapshot, install, sampleStatus, settle, text } from "./harness";
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
