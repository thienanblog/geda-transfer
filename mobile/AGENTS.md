# mobile/ — agent guide

The iOS app. Read the repository's [AGENTS.md](../AGENTS.md) first; this file
covers only what is specific to the phone.

Expo SDK 57 with a **custom development client**. Expo Go cannot load the
native module below, so `npx expo start` alone will not run this app.

## Layout

| Path | What it is |
|---|---|
| `src/core/` | Plain TypeScript, no React Native imports. All the decisions live here, and so do all the tests. |
| `src/data/` | Paired receivers (keychain) and the local record of what has been sent (SQLite). |
| `src/media/` | Reading the photo library. |
| `src/engine/` | Connecting, pairing, and running a transfer — foreground and background. |
| `src/ui/` | Screens. |
| `modules/geda-transfer/` | A local Expo module: Swift, `URLSession`, SPKI pinning, the background session. |
| `ios-extensions/` | The Live Activity widget. A second binary, not part of the app target. |
| `plugins/` | Config plugins. `ios/` is generated, so native project changes live here. |

## Why there is native code at all

Three reasons, all non-negotiable:

1. **Pinned TLS.** The receiver's certificate is self-signed and the app trusts
   exactly one public key (AGENTS.md §3.5). No JavaScript HTTP client on iOS
   can express that; a `URLSessionDelegate` can.
2. **Bytes must not cross the bridge** (AGENTS.md §3.8). `URLSession` reads the
   asset off disk and writes it to the socket. JavaScript decides what to send
   and draws the progress bar, and never sees a byte of the file.
3. **Background transfers happen without JavaScript.** Once a batch is handed
   to a background `URLSession`, the app can be killed; the system finishes the
   uploads and relaunches the app to say so, into a process that may have no
   React runtime at all. Everything that moment needs — the upload URL, the
   token, the staged file, the **pin** — is in `BackgroundStore`, on disk.

## The two transfer paths

|  | Foreground (`engine/uploader.ts`) | Background (`engine/background.ts`) |
|---|---|---|
| Session | default, 8 streams | `.background`, discretionary |
| Body | streamed from the library, at an offset | a **staged copy** in the app container |
| Resume | stream from the offset | write the remainder to its own file |
| Lives while | the app is open | the app is gone |
| Progress | per file, live | whatever arrives while the app is alive |

Neither is a fallback for the other. The foreground path is what saturates a
link; the background path is what finishes the job when someone walks away.

## Working on it

```bash
npm test          # vitest over src/core — runs anywhere, no device
npm run typecheck
npm run prebuild  # regenerate ios/ after changing app.json or native code
npm run ios       # build and run on a connected device or simulator
```

Native changes are **not** picked up by Fast Refresh; rebuild.

## Things that will bite you

- **`PHAsset` export is the bottleneck**, ahead of the network (AGENTS.md §5).
  That is why listing uses `exeForMetadata` — which never resolves a file path
  — and why resolving happens in a bounded pool, measured separately from the
  transfer.
- **Assets in iCloud are skipped, not downloaded.** Pulling gigabytes down over
  cellular to push them back up is not the transfer the user asked for.
- **Local Network permission** (iOS 14+) covers any connection to a
  local-subnet address, not just Bonjour. If it is denied, connections fail
  silently — surface it rather than showing a spinner.
- **Do not add a "trust anyway" button.** A pin mismatch is a hard failure; the
  recovery is scanning a fresh QR code, which requires being in front of the
  receiver.
- **A background task cannot read the photo library.** The bytes are sent by
  `nsurlsessiond`, which does not have this app's entitlement. A background
  upload can only send a file inside the app container, which is why staging
  exists and why it is not an optimisation to remove.
- **Never change the background session identifier.** `app.geda.transfer.upload`
  is how the system finds the app that owns an in-flight upload. Renaming it
  orphans every transfer that was running during the update.
- **`ios/` is generated.** Anything you would click in Xcode belongs in
  `plugins/`, or it disappears on the next `prebuild --clean`.
- **Do not turn `isDiscretionary` off to make a demo faster.** It is what buys
  the system's cooperation, and an app that fights the scheduler gets less of
  it, not more.
