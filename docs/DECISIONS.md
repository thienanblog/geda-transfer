# Decision Log

Append-only. Newest at the bottom. Each entry: what was decided, and why.

## 2026-08-12 — Scope: transfer, not sync
One-way, user-initiated transfer in both directions, plus opt-in auto-backup
Mobile→Desktop. Two-way sync was considered and **rejected**: it risks data
loss, requires conflict resolution and delete propagation, and the desktop side
may be a NAS holding the only copy. Deletion stays a manual, per-device action.

## 2026-08-12 — Transport: HTTP/2 over TLS, not a custom binary protocol
Driven by the top priority (ship on the App Store) plus the convenience
requirement. iOS background transfer requires background `URLSession`, which
speaks only HTTP. A custom protocol would forfeit background sync forever.
Accepted cost: slightly more per-request overhead, mitigated by HTTP/2
multiplexing and batching small files.

## 2026-08-12 — Transcoding on the desktop, not on the phone
On-device conversion is CPU-bound, drains battery, causes thermal throttling,
and discards originals. Originals are also smaller on the wire (HEIC vs JPEG).
An opt-in "convert on device" toggle remains for weak NAS receivers.

## 2026-08-12 — ffmpeg/libheif as external binaries only
Vendoring a GPL ffmpeg build would relicense the whole project as GPL, which
conflicts with App Store terms and blocks distribution. External process
invocation keeps the project Apache-2.0.

## 2026-08-12 — Cross-subnet discovery via unicast sweep + candidate sets
mDNS is multicast TTL=1 and cannot cross a router; WireGuard has no broadcast
domain. Therefore discovery across subnets must be unicast. The desktop
advertises every interface address including VPN addresses, and paired mobiles
race connections across the whole candidate set.

## 2026-08-12 — License: Apache-2.0, trademark reserved
Permissive (attribution via NOTICE), explicit patent grant, App Store
compatible. GPL/AGPL were **rejected**: they conflict with App Store terms
(cf. VLC). The names "Geda"/"Geda Transfer" and the logo are reserved under
Section 6 so forks cannot ship under the same identity.

## 2026-08-12 — Free, no monetization
No IAP, no license keys, no entitlement layer. Keeps the codebase clean and
fully open.

## 2026-08-12 — Name: Geda Transfer
"Sync" was dropped from the product name to avoid implying two-way sync.
App Store discoverability is handled by the separate Keywords field, which is
indexed but not user-visible.

## 2026-08-12 — Discovery probes are padded; announces are rate-limited
An unpadded ~60-byte probe eliciting a ~300-byte announce is a 5x UDP
amplification vector: spoof the source address and every receiver becomes a
DDoS reflector. Padding probes to ≥512 bytes pushes the factor below 1, and a
5/s per-source cap bounds the rest. Found while reviewing the draft spec.

## 2026-08-12 — Pin the SPKI, not the certificate; persist the key pair
A whole-certificate pin breaks on every renewal. Pinning
`SHA-256(SubjectPublicKeyInfo)` survives reissue from the same key pair. The
key is therefore stored in the OS keystore rather than the app bundle, so that
reinstalls do not produce spurious mismatches.

## 2026-08-12 — No "trust anyway" on pin mismatch
An override users can click is equivalent to no pinning at all. The only
recovery is re-scanning a QR code, which requires physical presence at the
receiver and so cannot be performed remotely by an attacker.

## 2026-08-12 — Pair basenames are allocated server-side, order-independent
`pair_basenames` keyed on `(device_id, pair_id)` with a primary-key race means
either member of a Live Photo pair may arrive first. Requiring the client to
send the primary first was rejected: background `URLSession` schedules tasks at
the OS's discretion and will not honour client ordering.
