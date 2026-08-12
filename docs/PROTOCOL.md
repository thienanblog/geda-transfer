# Geda Transfer Wire Protocol

Version `1`. This document is normative. If you change the wire format, update
this file in the same change.

Transport is **HTTP/2 over TLS 1.3**. See AGENTS.md §3.1 for why this is not a
custom binary protocol.

---

## 1. Endpoints and ports

| Purpose | Port | Protocol |
|---|---|---|
| Discovery probe/response | 47890 | UDP (unicast, broadcast, multicast) |
| Transfer + control | 47891 | TCP, HTTP/2 over TLS 1.3 |
| mDNS service | 5353 | `_gedatransfer._tcp` on 47891 |

Discovery multicast groups, used alongside broadcast because some segments
filter one and not the other:

| Family | Group |
|---|---|
| IPv4 | `239.192.71.90` (organisation-local scope) |
| IPv6 | `ff12::7a90` |

Ports are configurable. The defaults are what discovery assumes.

---

## 2. Discovery

### 2.1 Probe (client → receiver), UDP 47890

Sent as unicast (subnet sweep, candidate retry), broadcast, and multicast — in
parallel. Payload is JSON, one datagram.

```json
{ "v": 1, "t": "probe", "nonce": "<16 random bytes, base64>",
  "pad": "<zero bytes, base64>" }
```

**`pad` is mandatory.** The probe MUST be padded to at least 512 bytes, which
is larger than any announce. Without it the responder is a UDP amplifier: a
~60-byte probe with a spoofed source address would elicit a ~300-byte announce
aimed at the victim — a 5x amplification vector usable for DDoS. Padding drives
the amplification factor below 1 and removes the incentive entirely.

A receiver MUST silently drop any probe shorter than 512 bytes.

**One nonce per sweep round, not per host.** Sweeping a /24 sends 254 datagrams
that all share a single nonce. Simpler to track, and equally effective.

### 2.2 Announce (receiver → client), UDP 47890

Replies to the probe's source address. Also sent unsolicited on startup and
every 30s to the local broadcast address.

```json
{
  "v": 1,
  "t": "announce",
  "nonce": "<echoed from the probe>",
  "device_id": "<stable uuid>",
  "name": "Studio Mac",
  "platform": "darwin",
  "port": 47891,
  "spki": "<base64 SHA-256 of the TLS SPKI>",
  "addrs": ["192.168.11.20", "10.13.13.5", "fd00::5"],
  "paired": true
}
```

`addrs` is the **candidate set**: every address on every interface, including
`utun*`/`wg*` VPN addresses. This is what makes cross-subnet reconnection work
(AGENTS.md §3.4). Loopback and link-local are excluded.

`paired` reports that the receiver has at least one paired device. It is a UI
hint and nothing more.

`nonce` echo prevents off-path spoofing of announce packets. The client keeps
sent nonces in a set with a 10-second TTL and discards any announce that does
not match. Unsolicited announces — the periodic broadcast, which quotes no
nonce — are therefore ignored during a scan unless the client explicitly opts
in to them.

A probe set is sent **twice**, about 400ms apart. The first datagram to a host
whose hardware address is not yet resolved is dropped while the stack resolves
it, so a single round systematically misses hosts that are present.

A receiver MUST rate-limit announces to **5 per second per source address**.

**Discovery is not a security boundary.** It only produces hints about what
might be out there. Nothing is trusted until the TLS handshake verifies against
the pinned SPKI (§3.3). The nonce and the rate limit are defence in depth
against noise and abuse, not the thing that keeps an attacker out.

### 2.3 Candidate racing

A paired client with a stored candidate set opens TLS connections to **all**
candidates in parallel and keeps the first that completes a valid handshake
with the pinned SPKI; the rest are cancelled. Staggered start of 100ms between
candidates, ordered most-recently-successful first.

---

### 2.3.1 mDNS records (L1)

The service is `_gedatransfer._tcp.local.` on the transfer port. The instance
label is the receiver's display name plus the first 8 characters of its device
id, which makes it unique without implementing RFC 6762 conflict probing.

TXT keys: `v`, `id` (device id), `name`, `platform`, `spki`, and `paired=1`
when applicable. Addresses come from the A/AAAA records of the SRV target.

mDNS never leaves the local segment — multicast DNS is sent with TTL=1 and
routers drop it. Cross-subnet discovery is L3/L4 and nothing else.

### 2.4 `GET /v1/info`

Unauthenticated, because a client needs it to decide whether it is talking to
the receiver it paired with before it has a session.

```json
{
  "versions": [1],
  "device_id": "<uuid>",
  "name": "Studio Mac",
  "spki": "<base64 SHA-256 of SPKI>"
}
```

The `spki` field is a convenience for display and diagnostics only. **It is not
what establishes trust** -- the pin recorded at pairing time is, and it is
checked during the TLS handshake, before this response can be read.

---

## 3. Pairing

### 3.1 QR payload

The receiver renders this as a QR code. Compact JSON, then base64url.

```json
{
  "v": 1,
  "device_id": "<uuid>",
  "name": "Studio Mac",
  "spki": "<base64 SHA-256 of SPKI>",
  "addrs": ["192.168.11.20:47891", "10.13.13.5:47891"],
  "psk": "<32 random bytes, base64url>",
  "exp": 1786000000
}
```

The QR encodes `geda://pair/<base64url of the compact JSON>`. `addrs` carries
host:port so the client can dial without assuming a port.

`psk` is single-use and expires (`exp`, unix seconds, default +5 minutes). It
is held in memory on the receiver only: an offer that survived a restart would
be a credential outstanding without the user knowing. Presenting a secret spends
it, whether or not it turns out to be valid.

### 3.2 `POST /v1/pair`

Authenticated by the `psk`. The client pins `spki` on first connection (TOFU)
and refuses any later certificate that does not match.

Request:
```json
{ "v": 1, "psk": "...", "device_id": "<client uuid>",
  "name": "An's iPhone", "platform": "ios", "spki": "<optional>" }
```

`spki` is reserved for mutual TLS. It is recorded and not yet checked, so that
turning client certificates on later does not force everyone to re-pair.

Response:
```json
{ "token": "<opaque bearer token>", "device_id": "<receiver uuid>",
  "name": "Studio Mac", "spki": "<base64 SHA-256 of SPKI>",
  "addrs": ["192.168.11.20:47891"], "naming_template": "...",
  "max_concurrency": 8 }
```

`addrs` in the response supersedes the set in the QR code: a tunnel may have
come up since the code was rendered.

All later requests carry `Authorization: Bearer <token>`. Tokens do not expire;
they are revoked by unpairing on the receiver. Pairing a device that is already
known re-arms the existing row — a new token, the old history — so a user who
reinstalls the app does not lose their transfer record. The superseded token
stops working immediately.

### 3.3 What "pinning the SPKI" means

`SubjectPublicKeyInfo` is the substructure of an X.509 certificate holding only
the algorithm and the public key — no name, no validity dates, no signature.
The pin is:

```
pin = base64( SHA-256( DER encoding of SubjectPublicKeyInfo ) )
```

In Go that is `sha256.Sum256(cert.RawSubjectPublicKeyInfo)`.

Pin the SPKI, **not** the whole certificate. A self-signed certificate expires;
reissuing it from the same key pair produces a completely different certificate
but an identical SPKI, so the pin survives renewal and users never re-pair. A
whole-certificate pin would break on every renewal.

This replaces the role of a CA entirely: no third party vouches for the key,
because the user personally recorded the correct key by scanning the QR code.

### 3.4 Identity must be stable across reinstalls

A legitimate pin mismatch is a bug in disguise. The receiver MUST persist its
private key outside the application bundle so that updates and reinstalls keep
the same identity:

| Platform | Storage |
|---|---|
| macOS | Keychain, `kSecAttrAccessibleAfterFirstUnlock` |
| Windows | DPAPI (`CryptProtectData`), user scope |
| Linux / NAS | `~/.config/geda/identity.key`, mode `0600` |

When the certificate expires, generate a new one **from the existing key pair**.
`gedad identity export|import` exists for migrating a NAS to new hardware.

### 3.5 Handling a pin mismatch

A certificate that does not match the pinned SPKI is a **hard failure**. There
is no "trust anyway" affordance — an override that users can click is just a
CA that always says yes.

The only recovery path is scanning a fresh QR code from the receiver. That
requires physical presence at the machine, which a remote attacker cannot fake.

Client message:

> **Cannot verify "Studio Mac"**
> This computer's identity has changed since you paired with it. Usually this
> means it was reinstalled or replaced — but it can also mean something is
> impersonating it.
>
> To continue, open Geda Transfer on that computer and scan its QR code again.
>
> `[ Scan QR again ]  [ Cancel ]`

Both sides also display a human-readable fingerprint — the first 8 bytes of the
pin, grouped — so a user can compare by eye when a receiver has no screen:

```
Studio Mac
Fingerprint:  4F2A · 91C7 · B03D · 8E15
```

---

## 4. Dedup probe

Sent before any bytes, in batches. Lets the receiver say "already have it".

`POST /v1/have`
```json
{ "items": [
  { "id": "<client-local asset id>",
    "size": 4194304,
    "captured_at": "2026-07-04T10:22:31Z",
    "head_hash": "<BLAKE3 of the first 1 MiB, hex>" }
] }
```

Response:
```json
{ "results": [ { "id": "...", "have": true, "path": "2026/07/IMG_0042.HEIC" } ] }
```

`have: true` means the receiver already holds a file matching size +
capture date + head hash. The client skips it. This is the single largest win
on a repeat run — always batch it, never one round trip per file.

Full-file BLAKE3 is computed streaming during upload and verified on the
receiver; the head hash is a cheap pre-filter, not the authority.

---

## 5. Upload

Resumable uploads use the **tus** protocol (tus.io), v1.0.0, with the
`creation` and `checksum` extensions. Only the Geda-specific parts are
documented here.

### 5.1 Creation

`POST /v1/files` with `Upload-Length` and `Upload-Metadata`. Metadata keys are
base64-encoded per tus:

| Key | Meaning |
|---|---|
| `filename` | Original filename as it exists on the source device |
| `captured_at` | RFC3339, the asset's capture date |
| `hash` | Full-file BLAKE3, hex — when known up front |
| `album` | Source album name, if any |
| `pair_id` | **Groups files that must share a basename** |
| `pair_role` | `primary` \| `secondary` |
| `kind` | `photo` \| `video` \| `file` |

Two further keys, `device_id` and `device_name`, appear on stored uploads.
They are **set by the receiver from the authenticated token** and any values a
client sends are discarded. Trusting the client here would let one device file
its uploads under another device's identity.

**`pair_id` is mandatory for Live Photos (HEIC+MOV) and RAW+JPEG pairs.** The
receiver allocates the collision counter once per `pair_id`, never per file.
Getting this wrong silently breaks Live Photos.

#### Deriving `pair_id` on iOS

iOS already groups these — they are not separate assets. One `PHAsset` holds
several `PHAssetResource`s:

| Case | Assets | Resources |
|---|---|---|
| Live Photo | 1 | `.photo` (HEIC) + `.pairedVideo` (MOV) |
| RAW + JPEG | 1 | `.photo` (DNG) + `.alternatePhoto` (JPG) |
| Edited photo | 1 | `.photo` + `.fullSizePhoto` (rendered) |

So:

```
pair_id   = BLAKE3(PHAsset.localIdentifier)[:16]   // stable, does not leak the id
pair_role = primary    for .photo
            secondary  for .pairedVideo / .alternatePhoto
```

#### Allocating the basename on the receiver

Table `pair_basenames(device_id, pair_id, basename, PRIMARY KEY(device_id, pair_id))`.

On each upload creation carrying a `pair_id`, inside one transaction:

1. `SELECT` the basename for `(device_id, pair_id)`.
2. **Found** → reuse it, append only this file's extension.
3. **Not found** → render the naming template, then find a free counter by
   testing **`basename.*` as a glob across every extension** — not the exact
   filename. Checking only `IMG_0042.HEIC` would miss an unrelated pair's
   `IMG_0042.MOV` and produce a cross-pair collision. Then `INSERT`.
4. The `PRIMARY KEY` makes concurrent members of the same pair safe: the loser
   of the race re-reads the winner's basename.

Step 4 is what makes ordering irrelevant, and that matters: **the client must
not be required to send `primary` first.** A background `URLSession` schedules
tasks at the OS's discretion and will not honour any ordering the client
intends.

Both members get the same `mtime` (the capture date). If a member is skipped by
the dedup probe, the pair keeps its allocated basename regardless.

Response `201` with a `Location` for the upload.

### 5.2 Transfer

Standard tus `PATCH` with `Upload-Offset`. Resume by `HEAD`ing the location to
learn the current offset.

The client opens 6–8 concurrent uploads over one HTTP/2 connection. Small files
are pipelined — never wait for a per-file round trip.

### 5.3 Completion

On the final `PATCH`, the receiver verifies the full BLAKE3 against `hash`.

- Match → the file is committed, its mtime set to `captured_at`, and the
  response carries the final stored path.
- Mismatch → the upload is discarded and `460 Checksum Mismatch` is returned.
  The client retries once, then reports the file as failed.

The final `PATCH` response carries:

| Header | Meaning |
|---|---|
| `Geda-Stored-Path` | Destination-relative path, **base64 of UTF-8**. HTTP headers are Latin-1 by specification, so a path containing any non-ASCII character cannot be sent raw. |
| `Geda-Deduplicated` | `1` when identical content was already held. `Geda-Stored-Path` then points at the existing copy and nothing was written. |

`hash` in `Upload-Metadata` is optional. When absent the receiver still computes
the digest and records it, but has nothing to verify against -- so a client that
intends to delete its local copy **must** send it.

**A file is only eligible for delete-after-transfer once the receiver has
confirmed a full-hash match.** No exceptions.

---

## 6. Desktop → Mobile

A suspended iOS app cannot be pushed to (AGENTS.md §3.7). The mobile pulls.

`GET /v1/outbox` — called when the app comes to the foreground.
```json
{ "items": [
  { "id": "...", "filename": "contract.pdf", "size": 918273,
    "hash": "...", "kind": "file", "url": "/v1/outbox/<id>" }
] }
```

The client downloads via a background `URLSession`, which continues after the
app is suspended. `DELETE /v1/outbox/<id>` acknowledges receipt after the hash
is verified.

Routing on the client:
- `kind: photo|video` → Photo Library (default) or the Files container if the
  Advanced setting is on.
- `kind: file` → always the Files container.

The naming template does not apply to Photo Library saves — iOS does not permit
setting filenames there.

---

## 7. Errors

Standard HTTP status codes. Body is:

```json
{ "error": "checksum_mismatch", "message": "human readable", "retryable": false }
```

| Code | `error` | Retryable |
|---|---|---|
| 401 | `unauthorized` | no — re-pair |
| 409 | `pair_conflict` | no |
| 413 | `too_large` | no |
| 460 | `checksum_mismatch` | once |
| 507 | `insufficient_storage` | no |
| 503 | `busy` | yes, with backoff |

---

## 8. Versioning

`v` is bumped only on a breaking change. The receiver advertises supported
versions at `GET /v1/info`; clients pick the highest they both support.
