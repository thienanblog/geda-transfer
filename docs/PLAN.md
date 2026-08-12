# Implementation Plan

Ten phases. Each phase ends with something verifiable — usually a number.

Speed is the headline feature, so every phase from P3 onward carries a
measurement gate. Do not advance past a gate you have not measured.

---

## P0 — Foundation
- Go module `core/`, CI (build + test + vet), Apache-2.0 headers.
- SQLite schema: `devices`, `transfers`, `files`, `settings`.
- BLAKE3 streaming hasher with a benchmark.

**Gate:** `core/` builds and tests green with zero UI dependencies.

## P1 — Receiver
- HTTP/2 + TLS 1.3 server, self-signed cert generated on first run.
- tus resumable upload endpoint.
- Dedup probe endpoint: `(size, capture_date, first-1MB hash)` → have/haven't.
- Storage layout + filename template engine + collision policy.
- Pair-aware naming (Live Photo, RAW+JPEG share a basename).

**Gate:** `curl` can upload a file, resume it after a kill, and get skipped on
a repeat upload.

## P2 — Discovery + pairing
- L1 mDNS responder, L2 UDP broadcast responder.
- L3 unicast sweep (concurrent, 300ms timeout, user CIDR list).
- L4 candidate set: enumerate **all** interfaces incl. `utun*`/`wg*`.
- L5 QR pairing payload + manual host:port.
- TOFU SPKI pinning, per-device tokens.

**Gate:** with the desktop and the client on different subnets, reachable only
by unicast, discovery succeeds **in both directions** within 3 seconds.
This is the acceptance test for the original problem — treat it as blocking.

Verified by `scripts/verify-p2.sh`, which routes two Docker subnets through a
forwarding container. Use addresses reserved for documentation there, never
home-router ranges like 192.168.11.x: a Docker bridge overlapping the host's
own LAN takes the route away from it.

## P3 — CLI + Docker
- `gedad` headless daemon, config file, systemd unit.
- Multi-arch image (amd64 + arm64) for NAS.

**Gate:** container on a NAS receives an upload from `curl` on another subnet.

## P4 — Mobile: foreground upload
- Expo custom dev client, EAS build profile.
- Photo library enumeration, original resource export (no transcode path).
- Parallel `URLSession` uploads via `expo-file-system`, 6–8 concurrent.
- Progress UI, pause/cancel, `isIdleTimerDisabled` toggle.

**Gate:** measured MB/s on 200 mixed photos and one 4K video, recorded in the
repo. Establishes the performance baseline everything else is compared to.

## P5 — Mobile: background upload
- Background `URLSession`, `BGProcessingTask` kickoff on charge + Wi-Fi.
- Resume across app termination.
- Live Activity showing progress and ETA.

**Gate:** app force-quit mid-transfer; transfer completes on its own and the
result is verified by hash.

## P6 — Desktop app
- Wails v2 shell over `core/`, tray icon, autostart.
- Pairing QR, per-device folders, live transfer view, history.
- Full settings UI: destination, naming template, subnets, output formats.

**Gate:** a person who has never seen the app can pair and transfer without
instructions.

## P7 — Desktop → Mobile
- Mobile polls "anything for me?" on foreground; background download session
  continues the work.
- Media → Photo Library by default; Files container behind Advanced.
- Arbitrary (non-media) files always go to the Files container.

**Gate:** a 2GB ZIP lands in Files; a video lands in Photos, both verified.

## P8 — Format handling
- Mobile send options: Live Photo pairs, edited vs original, ProRAW, bursts,
  screenshots, hidden assets.
- Desktop output presets: Original / Compatible / Space-saving, plus the
  advanced per-type matrix.
- External ffmpeg/libheif detection with a clear message when absent.

**Gate:** Live Photo round-trips as a linked pair; ProRAW keeps its DNG.

## P9 — Delete-after-transfer (Advanced)
- Off by default, explicit warning, batched system confirmation.
- Only after receiver-confirmed hash match. Always to the OS trash.

**Gate:** deliberate failure injection never deletes an unverified file.

## P10 — Ship
- App Store submission: privacy manifest, background-mode justification,
  local-network permission copy, screenshots.
- Public repo, README, CONTRIBUTING with DCO.

---

## After v1
Android release · reverse browse of the desktop library from mobile ·
bandwidth throttling · SMB/WebDAV/S3 destinations · event-based auto-foldering
