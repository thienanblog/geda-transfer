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
| `src/engine/` | Connecting, pairing, and running a transfer. |
| `src/ui/` | Screens. |
| `modules/geda-transfer/` | A local Expo module: Swift, `URLSession`, SPKI pinning. |

## Why there is native code at all

Two reasons, both non-negotiable:

1. **Pinned TLS.** The receiver's certificate is self-signed and the app trusts
   exactly one public key (AGENTS.md §3.5). No JavaScript HTTP client on iOS
   can express that; a `URLSessionDelegate` can.
2. **Bytes must not cross the bridge** (AGENTS.md §3.8). `URLSession` reads the
   asset off disk and writes it to the socket. JavaScript decides what to send
   and draws the progress bar, and never sees a byte of the file.

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
