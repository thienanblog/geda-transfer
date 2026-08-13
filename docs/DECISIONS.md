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

## 2026-08-12 — The gate harness uses RFC 5737 ranges, and refuses to collide
The first version of the cross-subnet harness used the 192.168.11.0/24 and
192.168.12.0/24 addresses `docs/PLAN.md` uses to describe the gate. Those are
ordinary home-router ranges: on a machine whose own LAN is 192.168.11.0/24, the
Docker bridge took over the host's route to it and the developer lost their LAN
and their internet for as long as the network existed.

The harness now uses TEST-NET-2 and TEST-NET-3 (198.51.100.0/24 and
203.0.113.0/24), which are reserved for documentation and never appear on a
real network. `scripts/verify-p2.sh` also refuses to start when either subnet
overlaps an address on the host, because any hardcoded choice can be wrong on
somebody's machine, and finding out by losing connectivity is expensive.

## 2026-08-12 — gedad is configured by a plain `key = value` file, not YAML/TOML
The daemon is edited over SSH on a NAS, in whatever editor the box has, and
configured in Docker through environment variables. One flat namespace maps to
both without the user learning a second syntax: every key is also
`GEDA_<KEY>` and `-set key=value`. A structured format would have bought
nesting nobody needs and a dependency the CLI does not otherwise have.

Unknown keys are a **hard error**. A silently ignored `destination =` leaves
the destination at its default, and that is discovered days later with the
files in the wrong place.

## 2026-08-12 — Pairing on a headless box goes through a Unix control socket
Pairing offers live only in the memory of the running receiver, so `gedad pair`
cannot be a second process that reads the ledger — it has to ask the daemon.
The socket is in the state directory, 0600, owned by the daemon's user, and
that file permission is the whole authorisation boundary: reaching it means
being that user or root on that machine.

Rejected: an admin endpoint on the TLS port. Anyone who can reach the receiver
could then ask it for a pairing offer, which defeats a QR code whose entire
security property is that it requires physical presence.

## 2026-08-12 — The terminal QR code sets its own colours and checks the width
A QR code needs dark modules on a light background. Drawn in the terminal's own
palette it comes out inverted on the dark themes most people use, and many
scanners will not invert — so foreground, background, and the quiet zone are
all set explicitly. Modules are one character wide and half a character tall,
because terminal cells are about twice as tall as they are wide.

The code is also measured against the window before it is drawn: a QR code
wider than the terminal wraps, and a wrapped code cannot be scanned at all.
Too narrow prints the URI instead of a picture that will never work.

## 2026-08-12 — A bridged container must be told what to advertise
The daemon advertises every local interface address, which is right on a host
network and useless on a bridge: the only address it can see is a private
bridge address nothing outside can dial. So `advertise` exists, the Docker
compose file defaults to `network_mode: host`, and the bridged example sets
`GEDA_ADVERTISE` explicitly. Getting this wrong is invisible until after
pairing succeeds and no file ever transfers.

## 2026-08-12 — Naming templates are validated before they are stored
`{yyy}` renders literally rather than failing, so a typo would name every file
after the typo and nobody would notice until the photos were filed under it.
`naming.Validate` rejects unknown variables and unclosed braces, and renders
the template twice: once with everything present, once with only the original
filename — because a screenshot has no album and a file from the Files app has
no capture date, and a template built from those alone renders to nothing for
exactly the assets whose absence is hardest to spot.

## 2026-08-12 — The P3 gate runs the shipped image, not a test harness
The receiver in `test/nas/compose.yml` is `docker/Dockerfile` as published:
same entrypoint, same unprivileged user, no test code inside it. The client is
curl and openssl on a second Docker network, so every packet is routed, and the
script asserts the route is indirect before trusting anything else. It also
recomputes the SPKI pin from the served certificate with openssl — the same
check the phone makes — so the gate covers the pin, not only the upload.

## 2026-08-12 — The phone needs native code, and only for two things
Everything about which asset goes next, how many at once, and what the progress
bar says is TypeScript, where it is testable without a device. Two things
cannot be: the receiver's certificate is self-signed and pinned to one public
key, which no JavaScript HTTP client on iOS can express; and file bytes must
never cross the JavaScript bridge (AGENTS.md §3.8). Both are `URLSession` with
a delegate, so the app carries a small Swift module and nothing more.

The SPKI is taken out of the certificate's DER rather than rebuilt from the
public key. `SecKeyCopyExternalRepresentation` returns the bare key — for an EC
key just `04 || X || Y` — and reconstructing the algorithm header around it
means hardcoding one prefix per key type and silently computing the wrong
digest for anything else. `scripts/verify-p4.sh` checks the Swift and the Go
implementations agree on a real receiver certificate, because a disagreement
would make every pairing fail with a mismatch that has no override.

## 2026-08-12 — Listing is cheap, resolving is not, and they are timed apart
Getting a file path out of a `PHAsset` is the slowest step in a transfer, ahead
of the network (AGENTS.md §5). So listing uses `exeForMetadata`, which never
resolves a path, and resolution happens afterwards in a bounded pool. The two
phases are measured separately and both are recorded: a transfer rate that
looks excellent next to a much lower wall-clock rate is the signature of the
export being the bottleneck, and that is the thing worth optimising next.

## 2026-08-12 — Mobile skips the dedup probe for now and keeps a local record
The protocol's dedup probe needs a BLAKE3 of the first megabyte of each file
(docs/PROTOCOL.md §4), which on the phone means either moving those bytes
through the bridge — forbidden, and slower than sending them — or a native
BLAKE3, which arrives with the rest of the native hashing work. Until then the
app keeps its own record of what it has sent to each receiver, keyed on the
asset identifier *and its size*, so an edited photo is sent again. The receiver
still deduplicates by full hash on arrival, so a missing entry costs bandwidth
and never correctness.

## 2026-08-12 — Assets that live only in iCloud are skipped, never downloaded
Pulling gigabytes down over a cellular connection so they can be pushed back up
over Wi-Fi is not the transfer the user asked for, and on a metered plan it is
an expensive surprise. They are reported as skipped with a message that says
what to do about it.

## 2026-08-12 — The P4 gate cannot be automated, so the script says so
P4's gate is a measured figure over 200 photos and a 4K video. It has to come
from a physical iPhone on a real link: a simulator has no `PHAsset` export cost
and no radio, so a number from one would be a fiction that every later phase is
then compared against. `scripts/verify-p4.sh` checks everything around the
number — types, the tested core, the native sources, and that both sides agree
on the pin — and then fails while `docs/PERFORMANCE.md` holds no recorded run.

## 2026-08-12 — Docker isolates bridges, so the P3 harness needs a real router
The first version of the P3 harness put the client and the receiver on two
Docker networks and let the host route between them. It passed on the machine
it was written on and failed on Linux CI: Docker Engine installs
`DOCKER-ISOLATION` rules that drop forwarding between two bridge networks, so
no routing table on either side can get a packet across.

The harness now has a routing container attached to both networks, as the P2
one does. Every hop is within a single bridge — client to router, router to
receiver — and the crossing happens inside the router, which is what a home
router or a VPN gateway does. The router masquerades, so the receiver answers
on-link and needs no route of its own; that is what keeps the receiver
container exactly as shipped, with no added capabilities and no test code
inside it.

The failure was also unnecessarily confusing, and that is fixed too. The pin
check piped `openssl s_client` straight into a digest, and an `s_client` that
cannot connect prints nothing — so the pipeline produced the SHA-256 of the
empty string, which looks exactly like a pin. "Could not connect" was reported
as "wrong key". The certificate is now fetched on its own, its absence is a
distinct failure, and `s_client`'s stderr is in the message.

## 2026-08-12 — The identity permission check is Unix-only
`identity.key` is written 0600 and the test says so. On Windows there are no
Unix permission bits: Go synthesises a mode from the read-only attribute, so
every writable file reports 0666 and the assertion can only ever fail. What
protects the key there is the ACL on the user profile directory the state
lives in, which `os.Stat` does not report. The check is skipped on Windows
rather than weakened everywhere else.

## 2026-08-12 — Background uploads send a staged copy, not the library original
A background `URLSession` hands its tasks to `nsurlsessiond`, a system process,
and that process reads the body file itself — often hours later, with this app
long since terminated. It does not inherit the app's access to the photo
library, so a task pointed at a `PHAsset`'s original fails, silently and much
later, in a process nobody is watching.

So every background upload is copied into the app container first, and the
native side deletes the copy the moment the receiver confirms the file —
including when the app is not running, because the delegate runs in whatever
process the system launched to deliver the completion. Staging is bounded by a
file count, a byte budget, and half of the phone's free space, whichever is
smallest. Whatever does not fit waits for the next kickoff, once earlier copies
have been deleted.

The foreground path is unchanged and still streams straight from the library:
it runs inside this app, where the entitlement applies.

## 2026-08-12 — Everything a background upload needs is on disk, not in JavaScript
The system relaunches the app to say a task finished. That process may have no
React runtime, and starting one to answer "which upload was that?" would blow
the few seconds the system allows before it kills the app for dawdling.

So `BackgroundStore` is a plain JSON file holding, per job, the upload URL, the
token, the staged path, the size, the tus metadata — and the **SPKI pin**. The
pin is the one that matters: the TLS challenge for a background task is
answered by the app's delegate wherever it happens to be running, and a
delegate that cannot find the pin can only refuse. There is still no override
(AGENTS.md §3.5); the pin is on disk so it can be *checked*, not so it can be
skipped.

## 2026-08-12 — The background pin is checked per task, not per session
`URLSession` offers the TLS challenge to the session delegate or to the task
delegate, whichever is implemented, and only one of them. The background
session takes the task-level one on purpose: the task knows which upload it is,
so each job can be checked against the pin it was created with.

Answering per session would mean deciding from the address alone, and the store
outlives a re-pairing — one failed job carrying the previous key for the same
`host:port` would make every upload to that receiver unanswerable, for good.
The check itself is unchanged and still has no override.

## 2026-08-12 — Resuming a background upload means writing the remainder to a file
A background session can send a file and nothing else: no data body, no
streamed request. There is no way to say "start at byte 40,000,000". So a
resume asks the receiver for its offset, copies the rest of the staged file out
in 4 MB blocks, and sends that. It costs a second copy of the remainder, once,
on a path that only runs after an interruption — and the alternative is sending
the whole file again.

The foreground path does not do this. It streams from an offset, which is
cheaper and which only a default session allows.

## 2026-08-12 — The Live Activity is derived from disk, and says when it is stale
Without a push token, only the app can update a Live Activity — and during a
background transfer the app is usually not running. So the activity's contents
are computed from `BackgroundStore` rather than passed in from JavaScript,
which means the process the system launches to deliver a completion can update
it and end it with no runtime at all.

Between those wake-ups the figures do go stale, and each update carries a
`staleDate` twenty minutes out so the system dims the activity rather than
presenting a number that stopped being true an hour ago. The subtitle says
"continues in the background" rather than showing a countdown that keeps
stalling, because an estimate the system is free to ignore is not an estimate.

## 2026-08-12 — The widget extension is a config plugin, because ios/ is generated
`mobile/ios/` is produced by `expo prebuild` and is not checked in, so a target
added by hand in Xcode would vanish on the next clean prebuild. The Live
Activity's widget extension is therefore described in
`mobile/plugins/withLiveActivity.js`: it copies the sources in, adds the
target, embeds the product, and makes the app depend on it.

Two details cost an afternoon each and are worth writing down. Passing a
filename to `addBuildPhase` for the embed phase creates a file reference that
belongs to no group, and CocoaPods then refuses to rewrite the project at all
("no parent for object"); the target's own product reference has to be embedded
instead. And `addTargetDependency` does nothing — silently, no error — when the
project has no `PBXTargetDependency` section yet, which a single-target
generated project does not.

`GedaTransferAttributes.swift` is compiled into both binaries. ActivityKit
pairs the app's activity with the widget by the type's name, so the two
declarations must be identical, and one file copied into both targets is the
only way to guarantee that.

## 2026-08-12 — BGProcessingTask kicks off, it does not transfer
The registered handler runs on power and Wi-Fi, hands whatever is already
staged back to the background session, and returns. It does not enumerate the
photo library and it does not stage anything new: it gets a few seconds of a
budget the system may withdraw, and the transfer itself belongs to
`nsurlsessiond` regardless of whether the app is alive to watch it
(AGENTS.md §3.2). `requiresExternalPower` is what makes running it acceptable
at all.

## 2026-08-13 — The receiver's wiring moved to core/service, and gedad uses it
`cli/internal/daemon` assembled the ledger, the identity, the storage layout,
the HTTP server, and the discovery responders into one running receiver. The
desktop app needs exactly that, and copying it would have put the product's
behaviour in two places — where a fix to the discovery lifecycle lands on the
NAS and not on the desktop, and a state directory means something slightly
different depending on which binary made it.

So the wiring is `core/service` and both front ends run it. What stayed in
`cli/` is what only a headless box has: the `key = value` file and the control
socket. `control.Status`, `control.Offer`, and `control.Device` are now type
aliases for core's, so a field added in core cannot be silently missing from
`gedad status -json`.

## 2026-08-13 — The receiver publishes what it is doing, and never waits for a listener
A live transfer view cannot be built from the ledger: by the time a row exists
the interesting part is over. `core/events` is a bus the receiver publishes
each upload's lifecycle to.

One rule governs it: **a subscriber must never be able to slow a transfer
down**. Publishing is non-blocking and a subscriber that falls behind loses
events rather than applying back-pressure to the code writing bytes to disk. A
dropped progress event costs a stale percentage for a fraction of a second; a
blocked one costs throughput, which is the headline feature.

Progress is reported from *inside* the copy, not per request. One tus PATCH
carries a whole file, so without this a 4K video would move from 0% to 100% in
one step. It is rate-limited to five updates a second — past what anyone can
read, and cheap enough to be invisible.

## 2026-08-13 — The desktop has no config file; its settings live in the ledger
gedad has one because a NAS is administered over SSH. A desktop app is
administered through its own window, and a second file that the window and a
text editor could both write would only create a question about which of them
wins. The ledger already has to survive updates, so the settings live there,
next to the device id which is stored for the same reason.

The filename template is the exception, and deliberately: it stays under
core's own key, so that gedad and the desktop reading the same ledger cannot
disagree about what a template means.

## 2026-08-13 — Changing the destination restarts the receiver rather than swapping it
The receiver is handed its destination when it is built, and tusd holds it for
the life of an upload. Swapping it underneath a transfer in flight would write
half a file to each of two directories. Saving a new destination therefore
stops the service and opens another — which is quick, in-process, and costs
nothing, because the identity and the ledger are on disk and no phone has to
re-pair.

Only the naming template is applied in place: storage reads it from the ledger
on every commit.

## 2026-08-13 — Closing the window does not stop receiving, so there is a tray icon
A desktop cannot be woken by a phone: there is no push channel, and a receiver
that is not running is not discoverable at all. "Send from my phone" therefore
means "the app was already running", which is why closing the window leaves it
resident and why "Start when I log in" is offered on the first settings screen
rather than buried.

An app that stays resident after its window is dismissed and shows nothing is
indistinguishable from one that leaked. The tray icon is what makes it honest:
the app is visibly still there, it says what it is doing, and Quit is one
click. The tray is `systray.Register`, not `systray.Run` — Run creates its own
event loop, which on macOS means a second NSApplication fighting the window's.

## 2026-08-13 — The desktop's Go layer is testable without a window
`internal/app` does not import Wails. Everything the window can ask for is a
method that a test can call with no toolkit, and the three things that
genuinely need one — a folder picker, an event channel to the page, and the
tray — are interfaces satisfied by the shell in `main`.

That is what makes P6's gate scriptable at all. `internal/gate` drives those
same bindings with a real pinned TLS client standing in for a phone: it takes
the code off the first screen, decodes it as a camera would, redeems it,
uploads a file, and checks the bytes on disk. It is not a test of the window.
It is a test that there is nothing behind the window for the window to get
wrong.

## 2026-08-13 — The desktop files per device by default; gedad does not
`{device}/{yyyy}/…` is the desktop's default template because a desktop
receives from a household's worth of phones into a folder somebody browses in
Finder. That is P6's "per-device folders", and it is expressed as a template
rather than as a code path, so it goes through the same engine, the same
validation, and the same collision rules as anything a user types.

## 2026-08-13 — P6's gate is a person, and the script says which half it proves
"A person who has never seen the app can pair and transfer without
instructions" cannot be asserted by a script. `scripts/verify-p6.sh` asserts
everything that person depends on — the layering, the build, the tested logic,
a zero-configuration first run, a real pairing and upload through the app's
own bindings, and the settings a first-timer is most likely to change — and
then says plainly that the half which is a person is not claimed. That half is
recorded in docs/PERFORMANCE.md by somebody who watched one.

## 2026-08-13 — A development build keeps its own state directory
`wails dev` compiles with the `dev` build tag, so the separation is by
construction rather than by remembering to set an environment variable: the
state directory becomes `geda-dev`, the single-instance lock is qualified, and
the window is titled "(dev)".

It matters more here than in most apps. Pairing is the thing being worked on,
and pairing writes device rows and a TLS identity into the ledger. A developer
testing it against their real state accumulates junk devices in the app they
actually use — and a mistake that damages the identity makes every phone they
own fail with a pin mismatch that has no override (AGENTS.md §3.5).

`GEDA_STATE_DIR` is honoured exactly as given in both variants, unsuffixed:
somebody who named a directory meant that directory.

## 2026-08-13 — The window is tested too, in jsdom, against a stubbed bridge
The Go gate proves the receiver works. It cannot prove the window *says* so,
and P6's gate is about what a person reads. So the views have their own tests:
they run the real view code against a fake `window.go`, and assert the things a
first-time user depends on — that the first screen shows a code and where files
will go, that an empty transfer list says what to do next rather than only
reporting an absence, that "already had it" is distinguished from "stored" and
from "failed", that a refused setting shows its reason and keeps what was
typed, and that a cancelled folder picker does not wipe the destination.

One of them is a security test rather than a usability one: filenames and
device names come off a phone and are untrusted, so a hostile name must render
as text. `dom.ts` sets text through `textContent` and uses `innerHTML` in
exactly one place, with literals defined in that file.

jsdom rather than a real browser: every assertion is about structure and
wording, none of it depends on layout or on a rendering engine, and a browser
would buy nothing and cost a download in CI.
