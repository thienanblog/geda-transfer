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

`scripts/verify-p4.sh` reports this table as unmeasured while it has no row,
and `scripts/verify-p4.sh --require-measurement` fails on it — which is what to
run when claiming the phase is done. CI runs the reporting form, plus
`scripts/check-measurements.sh --strict` as a step that is allowed to be red:
no commit can produce this number, and a check nobody can turn green is a check
nobody reads. An unmeasured performance gate is still not a passed one.

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

---

## P7 gate — desktop → mobile

**The gate (docs/PLAN.md):** a 2GB ZIP lands in Files; a video lands in Photos,
both verified.

Two halves, as with P4 and P5. `scripts/verify-p7.sh` runs the protocol half in
full — a 2 GiB file queued on a real receiver, collected over pinned TLS by a
client that shares nothing with it but a key, interrupted at 40%, resumed by
range, and verified against the digest the receiver published — and then stops,
because the sentence names two places on a phone and no script can look in
them.

The transfer here is loopback, so the throughput it prints is a floor on the
code and says nothing about a network. The number worth recording below is the
one measured over Wi-Fi, from a computer to a phone.

### How to run it

1. Build the app and put it on a phone:
   ```bash
   cd mobile && npx eas build --profile development --platform ios
   ```
2. Pair it with the desktop app, then choose the phone and **Send files**: a
   2 GB ZIP and a video shot on a real camera (so it has a capture date that is
   not today).
3. Open the app on the phone. It will start collecting and say so.
4. **Close the app and put the phone down.** This is the half that matters: the
   download has to continue without it.
5. Open the app again. The files are verified and put away at this point, not
   before — only the app can write to the photo library.
6. Check both, by hand:
   - the ZIP in Files › On My iPhone › Geda Transfer › Received, with the name
     the computer had for it;
   - the video in Photos, filed under the day it was **shot**, not today.
7. Verify the ZIP, rather than trusting that it looks right:
   ```bash
   shasum -a 256 /path/on/the/computer/archive.zip
   ```
   and compare against the phone's copy shared back out through Files.

### Results

| Date | Device | iOS | Link | Receiver | ZIP size | Elapsed | Continued while closed | ZIP in Files | Video in Photos | Capture date kept | Hashes match | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| _no run recorded yet_ | | | | | | | | | | | | |

A table with only successes in it is not evidence. Record the run where the
phone was on cellular, or where the app was force-quit at the wrong moment, or
where the photo library permission had been denied — those are the paths this
direction actually fails on.

---

## P8 gate — format handling

**The gate (docs/PLAN.md):** Live Photo round-trips as a linked pair; ProRAW
keeps its DNG.

Unlike P4, P5, and P7, both halves of this sentence are decided by this
repository rather than by a phone, and `scripts/verify-p8.sh` checks both for
real: a Live Photo's HEIC and MOV uploaded as one pair to a receiver over
pinned TLS, and a ProRAW DNG beside them, under every output preset — including
the one whose whole purpose is to delete originals — and twice over, once with
no converter installed and once with one.

So there is nothing here that CI cannot prove. What is left for a device is
narrower and worth doing exactly once: that the *phone* produces what the
receiver is being tested against. iOS decides which `PHAssetResource`s a Live
Photo and a ProRAW shot actually have, and the only way to know the selection
rules match a real library is to point them at one.

Conversion timings belong here too. The receiver converts after receipt, on a
real CPU, and how long that takes on the machine somebody actually uses is the
difference between "Compatible" being a reasonable default to suggest and being
a setting that makes a laptop unusable for twenty minutes after every backup.

### How to run it

1. Build the app and put it on a phone:
   ```bash
   cd mobile && npx eas build --profile development --platform ios
   ```
2. On the phone, take **a Live Photo** and **a ProRAW shot** (Settings ›
   Camera › Formats › Apple ProRAW). Edit one of the Live Photos, so the
   edited-versus-original path is exercised too.
3. Install the converters on the computer, or note that you did not:
   ```bash
   brew install ffmpeg libheif
   ```
4. Send with the default preset (**Keep originals**), and look in the
   destination folder:
   - `…_IMG_xxxx.HEIC` and `…_IMG_xxxx.MOV`, the same name side by side.
     Dropping the pair into Photos on the Mac should produce one Live Photo,
     not two items.
   - `…_IMG_yyyy.DNG`, opening in Lightroom or Photoshop as a raw negative
     with its exposure still adjustable.
5. Switch to **Also save a copy anything can open** and send a second batch.
   A `.jpg` appears beside the HEIC, the HEIC is still there, and the DNG is
   untouched. Time how long the conversions take to drain.
6. Switch to **Convert and delete the original** and send a third batch. The
   Live Photo's two files must **still both be there** — a pair member is
   never replaced — and the DNG must still be there. A lone photo, not part of
   any pair, is the one that should have been replaced.
7. Verify rather than trusting that it looks right:
   ```bash
   shasum -a 256 '…_IMG_yyyy.DNG'
   ```
   against the DNG exported from the phone through Files.

### Results

| Date | Device | iOS | Receiver | Converters | Live Photo pair intact | ProRAW kept its DNG | Edited version correct | Preset | Conversion time | Originals kept | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| _no run recorded yet_ | | | | | | | | | | | |

A table with only successes in it is not evidence. Record the run where ffmpeg
was missing, where the phone had a burst of forty frames in it, where a ProRAW
shot had been edited, and where the destination already held a file with the
name a conversion wanted — those are the paths this phase actually fails on.
