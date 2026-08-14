# Privacy Policy

**Geda Transfer** · last updated 14 August 2026

This is the policy the App Store listing points at. It is short because the app
does very little that concerns anybody's privacy, and the short version is
this: **your files go from your phone to a computer you chose, and nowhere
else.**

## What is collected

Nothing. There is no account, no sign-in, no analytics, no crash reporting, no
advertising identifier, and no telemetry of any kind. No data about you or your
device is sent to the developer of this app, because there is no server to send
it to.

## Where your files go

To the receiver you paired with — your own computer or NAS — over your own
network, directly, encrypted with TLS 1.3. Nothing passes through a service
operated by us or by anybody else. There is no relay and no cloud storage.

If your computer is reachable over a VPN you run, the transfer goes through
that tunnel. That is still your infrastructure.

## What stays on the phone

- **The list of receivers you have paired with**, each with its name, its
  addresses, its public-key fingerprint, and an access token. The fingerprint
  and the token are held in the iOS keychain.
- **A ledger of what has been sent**, so the app does not send the same photo
  twice.
- **Temporary copies of files being transferred.** These live in the app's own
  container and are deleted when the transfer finishes or is cancelled.

All of it is removed when you delete the app.

## Permissions

- **Photos.** Read, to send what you select. The app never modifies an original.
  It writes to your library only when your computer sends something to the
  phone and only if you allow it.
- **Camera.** To scan a pairing QR code. Nothing is recorded or stored.
- **Local Network.** To reach your computer. iOS requires this permission for
  any connection to a device on your own network.

Each is asked for when it is first needed, and the app explains what it does
with it. Refusing one disables the feature that needs it and nothing else.

## Deleting photos from the phone

"Delete after transfer" is off unless you turn it on. When it is on, an asset
is deleted only after the receiver has read the stored file back and proved,
against a hash, that it holds every part of it — and iOS asks you to confirm.
Deleted assets go to Recently Deleted, where they remain recoverable for 30
days.

## Children

The app has no content and no social features. It is rated 4+.

## Changes

This file is versioned in the repository, so every change to it is a public
diff: https://github.com/thienanblog/geda-transfer/commits/main/docs/PRIVACY.md

## Contact

Open an issue at https://github.com/thienanblog/geda-transfer/issues, or, for
anything security-related, follow [SECURITY.md](../SECURITY.md).
