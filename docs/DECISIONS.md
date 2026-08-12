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

## 2026-08-12 — Uploads are hashed while they are written, not afterwards
The receiver folds each chunk into a BLAKE3 hasher as it lands, so the common
case of a single streamed PATCH needs no second pass. A resume that this
process has no hasher state for falls back to reading the finished file. On a
NAS the difference is a full extra read of every received video.

## 2026-08-12 — The upload's owning device comes from the token, not metadata
tus metadata is client-supplied. `device_id` and `device_name` are overwritten
from the authenticated session in PreUploadCreateCallback, otherwise any paired
device could file uploads under another device's identity.

## 2026-08-12 — The dedup probe is scoped to the requesting device
Each device has its own destination folder and its own history. A global probe
would let one phone's library suppress another phone's uploads.

## 2026-08-12 — Destination names are claimed with O_EXCL before the rename
A rename would silently replace a file the user placed in the destination
themselves, which the ledger knows nothing about. Claiming the name first turns
that into an ordinary collision and the file gets a counter instead.

## 2026-08-12 — tusd's logger is left at its default
tusd v2.10 types Config.Logger as golang.org/x/exp/slog.Logger, which is a
distinct type from the standard library's log/slog.Logger. Taking x/exp as a
direct dependency to satisfy one field is a worse trade than letting tusd log
through its own default.

## 2026-08-12 — Probes go to broadcast *and* multicast, on one fixed port
Some segments filter directed broadcast, others filter organisation-local
multicast, and which one is dropped is not knowable in advance. Sending both
costs two extra datagrams per scan. Groups are `239.192.71.90` and `ff12::7a90`.

## 2026-08-12 — A sweep sends every probe twice
The first datagram to a host whose hardware address is not yet in the
neighbour table is dropped while the stack resolves it. A single round
therefore misses hosts that are present, in a way that looks like the peer
being offline. Two rounds 400ms apart cost nothing and remove the failure.

## 2026-08-12 — A sweep is capped at 4096 hosts and refuses larger ranges
A /24 is 254 probes and finishes in about a second. A mistyped /8 would be
sixteen million. Refusing beats silently truncating, which would look like
discovery failing for hosts the user believes are in range.

## 2026-08-12 — The mDNS instance name carries the device id
RFC 6762 conflict probing exists to resolve two hosts claiming one name.
Qualifying the label with the first 8 characters of the device id means the
collision cannot arise, which is a great deal less code than implementing
probing and defending it.

## 2026-08-12 — Announces without a nonce are ignored during a scan
The periodic broadcast announce quotes no nonce, so accepting it during a scan
would reopen the off-path injection the echo closes. A client that wants to
listen passively opts in explicitly.

## 2026-08-12 — Candidate racing promotes the address that worked
After a successful handshake the winning address moves to the front of the
candidate set. Reconnecting then starts with the route known to be reachable
rather than paying the 100ms stagger for candidates that are not — which is the
usual case for a phone that stays on one network for weeks.

## 2026-08-12 — The client pins with InsecureSkipVerify plus a verify callback
Go offers no way to say "trust exactly this key". The CA and hostname checks are
disabled and replaced by an equality check against the pinned SPKI, which is
strictly stronger here: the certificate is self-signed and the receiver is
reached by whichever of its addresses works today, so a name match would prove
nothing.

## 2026-08-12 — Pairing offers live in memory and are spent on presentation
An offer that survived a restart would be a credential outstanding without the
user knowing. Redeeming deletes the secret before checking its expiry, so a
code that has been presented once cannot be presented again — including by
whoever photographed the screen.

## 2026-08-12 — Re-pairing updates the device row instead of replacing it
The files table points at the device, and a user who reinstalls the app expects
their history to still be there. Re-pairing issues a new token and revokes the
old one; nothing else about the device changes.

## 2026-08-12 — The P2 gate runs in Docker, with a negative control
Two Docker networks joined by a routing container reproduce the property under
test: the router forwards unicast and nothing else, so broadcast stops at the
boundary and mDNS — multicast with TTL=1 — never leaves either segment. This is
the same constraint a WireGuard tunnel imposes, and it is verifiable on any
machine with Docker rather than requiring two physical subnets.

The script also runs the reverse case: with the unicast sweep switched off and
mDNS and broadcast switched on, the peer must **not** be found. Without that
control a passing gate would only prove two containers can see each other.
