# Security Policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting:
[**Report a vulnerability**](https://github.com/thienanblog/geda-transfer/security/advisories/new).
It opens a private advisory only the maintainers can see.

Please do not open a public issue for a suspected vulnerability, and please do
not post a proof of concept anywhere public before there is a fix.

What helps: the version or commit of both sides, the platforms, whether the two
devices were on the same subnet or across a tunnel, and the smallest sequence
that reproduces it. If you have a patch, say so — you will get a co-author
credit and the fix will be faster.

Expect an acknowledgement within a few days. This is a small project run by
people with other jobs; there is no 24-hour on-call, and pretending otherwise
would be worse than saying so.

## Supported versions

The current release, and `main`. There are no long-term support branches.

## What is in scope

Anything that lets somebody who is not you read your files, write to your
devices, or impersonate a receiver you paired with. Specifically:

- **Breaking the pinning.** A phone accepts exactly one key per receiver:
  `SHA-256(SubjectPublicKeyInfo)`, pinned when you scan the pairing QR code. A
  way to get a connection accepted under a different key is the most serious
  bug this project can have.
- **Pairing.** The pairing secret is single-use and short-lived. A way to
  redeem one twice, to redeem one you did not see, or to pair without physical
  access to the receiver's screen.
- **Tokens.** Per-device bearer tokens. A way to obtain, forge, or reuse one
  across devices.
- **Path handling on the receiver.** Filenames arrive from another machine and
  are untrusted. A way to write outside the destination directory, or to make
  the receiver overwrite an existing file it should have renamed.
- **Deletion.** Delete-after-transfer removes files from a phone. Any way to
  make a receiver vouch for bytes it does not hold is a data-loss bug and is
  treated as the worst class of bug here, whatever its CVSS score would be.
- **Discovery amplification.** Probes are padded and announces are rate-limited
  precisely so this service cannot be turned into a UDP reflector. A way around
  either is in scope.
- **The key at rest.** The receiver's private key belongs in the OS keystore, or
  in a `0600` file where there is none. A way to read it as another local user
  is in scope.

## What is not

- **Anyone with physical access to an unlocked device.** They can pair a phone
  by looking at the screen. That is what pairing is.
- **Discovery telling you a receiver exists.** Discovery is a hint, not a
  security boundary. Nothing is trusted until the pinned key verifies, and the
  probes carry no secrets.
- **A self-signed certificate**, or any tool that reports it as one. There is no
  CA in this design on purpose: trust is the key you pinned, not a signature
  from a third party. See [AGENTS.md](AGENTS.md) §3.5.
- **The absence of a "trust anyway" button** on a pin mismatch. That button is a
  CA that always says yes. A mismatch is a hard failure, and the recovery is to
  scan a fresh code while standing in front of the receiver. Reports asking for
  an override will be closed with this paragraph.
- **Anything requiring a compromised receiver to attack its own user.** If the
  computer receiving your photos is hostile, it already has your photos.

## Design notes worth knowing before you look

The whole trust model is about 400 lines and lives in the pairing and TLS paths:
`core/pairing/`, `core/receiver/`, and `mobile/modules/geda-transfer/ios/`
(`SPKIPin.swift`, `PinnedClient.swift`). The reasoning behind it is in
[AGENTS.md](AGENTS.md) §3.5 and [docs/DECISIONS.md](docs/DECISIONS.md); the wire
format is [docs/PROTOCOL.md](docs/PROTOCOL.md), which is normative.

There is no account system, no password, no server-side component, and no
telemetry. There is nothing to breach on our side because there is no our side.
