# Geda Transfer — Agent Guide

Fast Wi-Fi transfer of photos, videos, and files between mobile devices and a
desktop/NAS. Local-first, no cloud, no account.

**Read this file before touching code.** It records decisions that are already
settled — do not relitigate them, and do not "improve" them without asking.

---

## 1. What this app is (and is not)

| It IS | It is NOT |
|---|---|
| One-way transfer, user-initiated, in both directions | Two-way sync |
| Auto-backup Mobile → Desktop (opt-in) | A cloud service |
| Local network only (incl. VPN/WireGuard) | A relay/proxy service |
| Photos, videos, **and arbitrary files** | Photos-only |

**Never implement bidirectional sync, conflict resolution, or delete
propagation.** Deletion is always an explicit user action on one device only.

---

## 2. Repository layout

```
core/       Pure Go. Receiver, transfer engine, discovery, pairing, storage,
            naming, dedup ledger. MUST NOT import Wails or any UI package.
desktop/    Wails v2 app (macOS + Windows). Thin UI over core/.
cli/        gedad — headless daemon for NAS/Linux. Thin CLI over core/.
docker/     Multi-arch image (amd64 + arm64) wrapping cli/.
mobile/     Expo app (iOS first; Android later, same codebase).
docs/       PROTOCOL.md, PLAN.md, DECISIONS.md
```

**Hard rule:** `core/` is the single source of truth for all behavior.
`desktop/`, `cli/`, and `docker/` are presentation layers. If you are about to
put logic in `desktop/`, it belongs in `core/` instead.

---

## 3. Settled architectural decisions

### 3.1 Transport is HTTP/2 over TLS 1.3. Not a custom binary protocol.

**Why:** iOS background transfers are only possible through
`URLSession(configuration: .background)`, and that API speaks **only HTTP**. A
custom socket protocol would permanently forfeit background sync — the single
most important convenience feature. This is non-negotiable.

Resumable uploads use the **tus** protocol (`tusd` on the Go side).

### 3.2 Two transfer modes, one protocol

| Mode | When | Mechanism |
|---|---|---|
| Foreground | App is open | Default `URLSession`, 6–8 parallel tasks, HTTP/2 multiplexing, max throughput. `isIdleTimerDisabled` while active (user toggle). |
| Background | App suspended/killed | Background `URLSession` (discretionary). Slower, OS-scheduled, but survives app termination. |

`BGProcessingTask` is used only to *kick off* a background session when the
device is charging + on Wi-Fi. It is not the transfer mechanism.

### 3.3 Mobile always sends originals. Transcoding happens on the desktop.

Converting HEIC→JPEG or HEVC→H.264 **on the phone** is slow, drains battery,
triggers thermal throttling, and destroys the original. Sending the original is
faster *and* smaller (HEIC ≈ 50% of JPEG).

- Phone: bit-exact passthrough of the original asset resource.
- Desktop: optional transcode after receipt, in parallel, on real CPU.
- A "Convert on device" toggle exists in Advanced settings, default **off**,
  with an explicit slowness warning. Only for weak NAS targets.

**ffmpeg/libheif must be invoked as a separate external binary, never linked
or vendored into the Go build.** A GPL ffmpeg build linked into the binary
would force the entire project to GPL, which breaks Apache-2.0 and blocks App
Store distribution.

### 3.4 Discovery is five layers, run in parallel, results merged

| Layer | Mechanism | Reaches |
|---|---|---|
| L1 | mDNS/Bonjour `_gedatransfer._tcp` | Same subnet |
| L2 | UDP broadcast + multicast, fixed port | Same subnet (mDNS fallback) |
| L3 | **Unicast subnet sweep** over user-supplied CIDRs | **Across subnets** |
| L4 | **Candidate-set retry** for already-paired peers | Anywhere |
| L5 | Manual `host:port` / hostname / **QR pairing** | Anywhere |

**Why L3/L4 exist — read this before "simplifying" discovery:**
mDNS is multicast with **TTL=1**; routers drop it by design. WireGuard is a
point-to-point L3 tunnel with **no broadcast domain at all**. So mDNS can never
cross subnets, regardless of firewall rules. Cross-subnet discovery **must** be
unicast.

- **L3:** sweep a /24 with ~254 concurrent probes, 300ms timeout → done in
  1–2s. Cheap. Do not serialize this.
- **L4:** the desktop enumerates **every interface address, including
  `utun*`/`wg*` VPN addresses**, and advertises them all as a candidate set.
  Paired mobiles store the whole set and race connections across all of them
  (Happy Eyeballs style; first responder wins, cancel the rest). This is what
  makes a phone on a remote subnet reach a desktop over WireGuard without any
  user configuration.

The protocol is **symmetric** about who initiates. If A→B is firewalled but
B→A works, whichever side can connect initiates the session.

### 3.5 Security: TLS + trust-on-first-use

- Self-signed cert on the receiver; the mobile **pins the SPKI at pairing time**.
  SPKI = the algorithm + public key substructure of the cert. Pin
  `SHA-256(cert.RawSubjectPublicKeyInfo)`, **never the whole certificate** —
  reissuing from the same key pair keeps the SPKI stable, so renewal does not
  force everyone to re-pair.
- The receiver's private key lives in the OS keystore (Keychain / DPAPI /
  `~/.config/geda/identity.key` 0600), **not** in the app bundle. Identity must
  survive updates and reinstalls, or users hit spurious pin mismatches.
- Pairing via QR code containing `{device_id, spki_pin, candidate_addrs[], psk}`.
- Per-device token thereafter. No accounts, no passwords, no CA.
- **A pin mismatch is a hard failure with no override.** Recovery is only by
  scanning a fresh QR, which requires physical presence at the receiver. Never
  add a "trust anyway" button — it is a CA that always says yes.
- **Discovery is not a security boundary.** It produces hints only; nothing is
  trusted until the pinned SPKI verifies.
- Discovery probes are **padded to ≥512 bytes** and receivers **rate-limit
  announces to 5/s per source**. Without padding an unpadded probe with a
  spoofed source turns every receiver into a 5x UDP amplifier.

### 3.6 Storage, naming, dedup

- Ledger: **SQLite** on both sides.
- Hashing: **BLAKE3**, computed streaming during read (never read a file twice).
- Pre-transfer dedup probe: send `(size, capture_date, hash_of_first_1MB)`;
  receiver answers "already have it" → skip. Huge win on re-runs.
- Filename templates, configurable on both sides. Variables:
  `{yyyy} {MM} {dd} {HH} {mm} {ss} {original_name} {device} {album} {counter} {hash8} {ext}`
  Default: `{yyyy}-{MM}-{dd}_{HH}{mm}{ss}_{original_name}.{ext}`
- Collision policy: **same hash → skip (identical file). Different hash →
  append `_1`, `_2`, …**
- **Live Photo (HEIC+MOV) and RAW+JPEG pairs must share one basename.** The
  collision counter is allocated **per pair**, never per file. `pair_id` comes
  from the `PHAsset.localIdentifier` — iOS already groups these as one asset
  with several resources. The receiver keys a `pair_basenames` table on
  `(device_id, pair_id)` and probes for free names with a **`basename.*` glob
  across all extensions**, not an exact-filename check. Ordering must not
  matter: background `URLSession` schedules tasks when it pleases, so never
  require the primary to arrive first.
- File mtime on the receiver is set to the asset's **capture date**, not the
  transfer time.

### 3.7 Platform constraints you must design around

- **iOS Local Network permission** (iOS 14+) applies to *any* connection to a
  local-subnet address, not just mDNS. Requires `NSLocalNetworkUsageDescription`
  and `NSBonjourServices` in Info.plist. If denied, subnet sweep fails
  silently — detect and surface this to the user.
  Note: connections to a **WireGuard/VPN address are not local**, so they are
  exempt from this prompt.
- **iOS cannot set filenames in the Photo Library.** For Desktop→Mobile, media
  goes to the Photo Library by default (best UX) and the naming template simply
  does not apply there. Saving to the app's Files container preserves names but
  is an **Advanced setting**, off by default — ordinary users do not understand
  where those files live or that they consume storage.
- **Desktop cannot push to a locked/suspended iPhone.** Desktop→Mobile works by
  the mobile asking "anything for me?" when the user opens the app; a background
  download session then continues the work. No APNs, no push server.
- **Deleting photos on iOS always shows a system confirmation dialog.** Batch
  deletions into one prompt. Delete only after the receiver confirms a matching
  hash. Deleted assets go to Recently Deleted (30 days).
- **Android** (later): `MediaStore.createTrashRequest()` for the 30-day trash;
  `READ_MEDIA_IMAGES`/`READ_MEDIA_VIDEO` on 13+; foreground service of type
  `dataSync` with a visible notification on 14+.

### 3.8 Never move bytes through the JS bridge

On mobile, file data must never be read into JavaScript. Use native-backed
upload/download (`expo-file-system`) so bytes flow inside the native layer. JS
orchestrates and renders progress only. Violating this destroys throughput.

---

## 4. Conventions

- **Go:** standard layout, `gofmt`, errors wrapped with `%w`, no panics outside
  `main`. Concurrency via `errgroup`. Contexts threaded through everything —
  every transfer must be cancellable.
- **Mobile:** TypeScript strict. Expo with a **custom dev client** (Expo Go
  cannot do mDNS or native sockets). Builds via EAS.
- **Settings:** every user-facing setting has a sane default and works with
  zero configuration. Advanced settings are behind a clearly separated section
  with warnings where data loss is possible.
- **Destructive defaults are off.** "Delete after transfer", "convert on
  device", and "save to Files" all default to off.
- **Licensing:** every new source file gets the Apache-2.0 header. Do not add
  dependencies under GPL/AGPL — they are incompatible with App Store
  distribution.

## 5. Performance targets

Speed is the headline feature. Measure, don't guess.

- Foreground transfer must saturate a typical Wi-Fi 6 link on large files.
- The realistic bottlenecks, in order: (1) `PHAsset` resource export on iOS,
  (2) per-file overhead on many small files, (3) the network. Optimize in that
  order.
- Batch/pipeline small files; never wait for a per-file round trip.
- Every performance claim in a PR needs a before/after number.

## 6. Workflow: one branch per phase

Phases are defined in `docs/PLAN.md`. Work through them **in order**, one at a
time. For each phase:

1. `git checkout -b phase/pN-<slug>` off an up-to-date `main`.
2. Implement the phase.
3. **Run the full test suite** — `go test ./...` in `core/`, plus whatever the
   phase added. It must be green.
4. **Code review the diff** before merging. Fix what the review finds; re-run
   the tests after fixing.
5. **Verify the phase gate** from `docs/PLAN.md`. A gate that cannot be
   verified is a phase that is not done — say so rather than merging anyway.
6. Merge to `main` with `--no-ff` so the phase stays visible in history.
7. Only then branch for the next phase.

Never work on two phases in one branch. Never merge a red test suite. If a gate
fails, fix it in the same branch or explicitly report that the phase is blocked.

Commits are signed off (`git commit -s`) for the DCO.

## 7. Before you finish a change

- `core/` builds and tests pass without any UI dependency.
- No new GPL/AGPL dependency.
- Destructive paths still default to off and still verify hashes first.
- If you changed the wire format, update `docs/PROTOCOL.md` in the same change.
- If you made an architectural decision, append it to `docs/DECISIONS.md`.
