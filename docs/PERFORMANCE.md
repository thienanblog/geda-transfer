# Measured performance

Speed is the headline feature, so it is measured rather than asserted
(AGENTS.md §5). Every number here came from a real device on a real network.
A change that claims to make something faster belongs in this file with a
before and an after.

---

## P4 gate — mobile foreground upload

**The gate (docs/PLAN.md):** MB/s over 200 mixed photos and one 4K video,
recorded here. Everything after P4 is compared against this baseline.

### How to run it

1. Start a receiver and note its name:
   ```bash
   docker compose -f docker/compose.yml up -d
   ```
2. Build a development client onto a physical iPhone. A simulator cannot
   produce this number — it has no `PHAsset` export cost and no radio, so a
   figure from one would be a fiction that every later phase is compared
   against.
   ```bash
   cd mobile && npx eas build --profile development --platform ios
   ```
3. Pair the phone with the receiver (`gedad pair`, then scan).
4. In the app: **Run the benchmark**. It sends the 200 newest photos and the
   newest video, having first cleared this phone's record of what that
   receiver already holds, so nothing is skipped.
5. Share the result and paste the row into the table below.

### Results

Two rates, because they answer different questions. **Transfer** is the link
while bytes are moving. **Wall clock** includes the time spent getting files
out of the photo library before any byte moves — which is what the person
holding the phone actually waits for, and which AGENTS.md §5 predicts is the
first bottleneck.

| Date | Device | iOS | Link | Receiver | Files | GB | Streams | Library (s) | Transfer (s) | Transfer MB/s | Wall clock MB/s | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| _no run recorded yet_ | | | | | | | | | | | | |

`scripts/verify-p4.sh` fails while this table has no row: an unmeasured
performance gate is not a passed one.

---

## P5 gate — mobile background upload

**The gate (docs/PLAN.md):** the app is force-quit mid-transfer, the transfer
completes on its own, and the result is verified by hash.

`scripts/verify-p5.sh` checks the half of this that a script can: an upload
interrupted part way through is resumed **by a second, independent client**
that shares nothing with the first but the tus URL — which is exactly the
situation after a relaunch — and the stored file's SHA-256 matches the source.

The other half needs a phone. No simulator runs `nsurlsessiond` faithfully, and
no script can swipe an app away.

### How to run it

1. Start a receiver:
   ```bash
   docker compose -f docker/compose.yml up -d
   ```
2. Build onto a physical iPhone:
   ```bash
   cd mobile && npx eas build --profile development --platform ios
   ```
3. Pair, then choose **Send in the background**. Wait for "queued with iOS".
4. **Swipe the app away** from the app switcher while files are still going.
5. Leave the phone on Wi-Fi, ideally on charge. The Lock Screen activity keeps
   showing progress; it dims when its figures go stale, which is expected
   between the system's wake-ups.
6. Reopen the app. Every file should be marked as sent.
7. On the receiver, hash the arrivals and compare against the originals:
   ```bash
   find /destination -type f -newer /tmp/marker -exec shasum -a 256 {} +
   ```
8. Paste the row below.

### Results

Background transfers are **discretionary**: the system decides when to spend
the radio, so the elapsed time is a property of the phone's day — battery,
thermals, whether it was charging — and not of this code. It is recorded
anyway, because a background transfer that takes ten times longer than the
foreground one is still the right trade and a hundred times longer is not.

| Date | Device | iOS | Link | Receiver | Files | GB | Killed after | Elapsed | Completed | Hashes verified | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| _no run recorded yet_ | | | | | | | | | | | |

The P4 baseline above is the comparison: background is expected to be slower,
never wrong.

---

## P6 gate — desktop app

**The gate (docs/PLAN.md):** a person who has never seen the app can pair and
transfer without instructions.

This one is not a number, and it is the only gate whose subject is a person.
`scripts/verify-p6.sh` checks everything that person depends on — the layering,
the build, the tested logic, a zero-configuration first run, and a real pairing
and upload driven through the same bindings the window calls — and then stops,
because no script can watch somebody get stuck.

### How to run it

Find somebody who has not seen the app. A person who has watched it being built
cannot un-know where the button is.

1. Build it and install it on a machine they have not used:
   ```bash
   cd desktop/frontend && npm ci && npm run build
   cd .. && wails build
   ```
2. Put Geda Transfer on a phone and pair nothing in advance.
3. Hand both over. Say only: "send a photo from the phone to this computer."
4. **Say nothing else.** Every question they ask is a row in the table below,
   and answering it destroys the measurement.
5. Watch for the four places this can fail: finding how to start pairing,
   getting the code scanned, believing the transfer worked, and finding the
   file afterwards.
6. Record what happened, including a run where they got stuck — a table with
   only successes in it is not evidence.

### Results

| Date | Observer | Subject has used it before | OS | Time to paired | Time to first file | Questions asked | Got stuck on | Found the file unaided | Notes |
|---|---|---|---|---|---|---|---|---|---|
| _no run recorded yet_ | | | | | | | | | |

Until this table has a row, P6's gate is met in everything the app does and
unverified in the one thing it is actually about.
