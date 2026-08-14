# App Store submission

Everything App Store Connect asks for, with the answer and the reason for it.
Written down because most of these questions have one correct answer for this
app and a plausible wrong one, and the wrong one is only discovered a week
later in a rejection email.

The app is **Geda Transfer**, bundle identifier `app.geda.transfer`, iOS and
iPadOS, free, no accounts, no in-app purchases, no analytics, no advertising,
and no SDK from anybody whose business is data.

Nothing here is generated: if you change `mobile/app.json`, change this file in
the same commit. `scripts/verify-p10.sh` checks the parts that can be checked.

---

## 1. What the reviewer has to be able to do

**This is the highest risk in the whole submission.** Geda Transfer moves files
between a phone and *the user's own computer*. A reviewer who opens the app
with no receiver on the network sees an empty screen and a pairing button, and
an app that cannot be exercised is rejected under Guideline 2.1 — App
Completeness.

So the submission carries, in App Review Notes:

1. **A receiver the reviewer can run.** The daemon is a single static binary
   with no dependencies and no installer; the notes link to a notarised macOS
   build and to the Docker one-liner, and say that either takes under a minute.
   The reviewer's Mac and the review iPhone must be on the same Wi-Fi.
2. **A pairing code**, which the receiver prints in its own terminal — so no
   demo account, no credentials, nothing to expire.
3. **A demo video** of the whole flow on real hardware, because parts of it
   (background transfer surviving a force-quit, the Live Activity on the Lock
   Screen) cannot be shown to somebody who has only the app.

Do not submit with "requires additional hardware" as the whole explanation. It
is true, and it is the sentence that gets an app rejected rather than tested.

## 2. Store listing

| Field | Value |
|---|---|
| Name | Geda Transfer |
| Subtitle | Photos to your own computer |
| Category | Primary: Utilities. Secondary: Photo & Video |
| Age rating | 4+ |
| Price | Free |
| Support URL | https://github.com/thienanblog/geda-transfer |
| Marketing URL | https://github.com/thienanblog/geda-transfer |
| Privacy policy URL | The published copy of [PRIVACY.md](PRIVACY.md) |
| Copyright | 2026 Geda |

**Description** — the submitted text lives here so that a change to it is a
reviewable diff:

> Geda Transfer copies photos, videos, and files from your iPhone to your own
> computer or NAS over Wi-Fi. Nothing goes to a cloud service, there is no
> account, and nothing is uploaded anywhere you did not point it at.
>
> Originals stay originals. HEIC arrives as HEIC, ProRAW keeps its DNG, and a
> Live Photo arrives as the pair it left as. If you would rather have JPEG or
> H.264, your computer converts after the files land — on a real processor,
> without draining the phone.
>
> Start a transfer and put the phone down: it keeps going while the app is
> closed, and picks up where it stopped if it is interrupted. A Live Activity
> shows how far along it is.
>
> Your computer can send files back the same way. Photos and videos go to your
> library, everything else to the Files app.
>
> Pairing is a QR code your computer shows. The phone remembers that computer's
> key and will talk to nothing else — the connection is encrypted end to end and
> never leaves your network.

**Keywords** (100 characters, comma-separated, no spaces after commas):

```
wifi transfer,photo backup,nas,heic,raw,local network,file transfer,no cloud,import,offline
```

Rules that bite here: no other platform may be named in the description
(Guideline 2.3.10), "free" may not be used in the app name, and the subtitle is
30 characters. The word "backup" is fine; "iCloud" would not be.

## 3. App Privacy — "Data Not Collected"

Every category is answered **not collected**, and that is not a stretch of the
definition. Apple's definition of collection is transmitting data off the
device *to the developer or a third party*. This app transmits photos from one
of the user's devices to another of the user's devices, chosen by the user,
over their own network. There is no server operated by anybody else in the
path, no analytics, no crash reporter, and no third-party SDK.

Answer the questionnaire this way:

| Question | Answer |
|---|---|
| Do you or your third-party partners collect data from this app? | No |
| Tracking | No. The app has no ad identifier, no ATT prompt, and no tracking domains |

If a future version adds a crash reporter, this section is wrong and the
questionnaire has to change with it. That is the point of writing it down.

## 4. Privacy manifest

`mobile/app.json` → `ios.privacyManifests`. Prebuild writes it to
`ios/GedaTransfer/PrivacyInfo.xcprivacy` and adds it to the app target.

| Key | Value | Why |
|---|---|---|
| `NSPrivacyTracking` | `false` | No tracking, no ATT prompt |
| `NSPrivacyTrackingDomains` | empty | Nothing to declare when tracking is false |
| `NSPrivacyCollectedDataTypes` | empty | §3 |
| `NSPrivacyAccessedAPITypes` | `FileTimestamp` → `C617.1` | The app reads the size and metadata of the staged copies it makes inside its own container. `C617.1` is the reason Apple defines for exactly that |

Everything else in the binary that touches a required-reason API belongs to a
dependency, and each ships its own manifest: `expo-file-system`
(`FileTimestamp`, `DiskSpace`), `expo-media-library` (`FileTimestamp`),
React Native (`FileTimestamp`, `UserDefaults`). Apple merges them; the app does
not restate them.

The Live Activity extension has no manifest because it calls no required-reason
API — it draws a progress bar from the attributes the app hands ActivityKit.

Under-declaring is answered by an automated ITMS-91053 email a few minutes
after upload, so if that arrives, the fix is here and not in a resubmission of
the same binary.

## 5. Export compliance

`ITSAppUsesNonExemptEncryption` is `false`, and the app does use encryption.
Those are consistent: the question is whether the app uses *non-exempt*
encryption, and everything cryptographic in this app is the operating system's
own — TLS 1.3 through `URLSession`, and SHA-256 through CryptoKit. Certificate
pinning is a comparison of a hash the OS computed; it is not an implementation
of anything.

No CCATS, no year-end self-classification report, no French declaration.

## 6. Background modes

The only declared mode is `processing`, and the only registered task
identifier is `app.geda.transfer.kickoff`.

**What it is for.** When the user has turned automatic backup on, iOS is asked
for a `BGProcessingTaskRequest` that requires external power and network
connectivity. When the system grants it — typically overnight on the charger —
the task does one thing and returns: it hands the pending files to a background
`URLSession`. The upload itself is not done inside the task and does not depend
on it.

**What is not declared, and why that is correct.** Background `URLSession` is
the actual transfer mechanism and needs *no* entry in `UIBackgroundModes`: the
transfer belongs to `nsurlsessiond`, a system process, which relaunches the app
to deliver the result. Declaring `fetch` for it — a common cargo-culted mistake
— would be asking for a capability the app never uses, which is a rejection
under Guideline 2.5.4. `audio`, `location`, and `voip` are likewise absent, and
none of them may be added to keep a transfer alive; the correct mechanism is
already in place.

Live Activities need no background mode either. The app updates the activity
while it is running, and the system keeps drawing it afterwards.

If review asks why `processing` is needed at all: without it the app can only
transfer while it is open, which for a photo library is the difference between
a feature and a demonstration.

## 7. Permissions, and what happens when they are refused

Purpose strings live in `mobile/app.json` and nowhere else. Every one of them
says what the app does with the data and why; a string that merely repeats the
name of the permission is the classic 5.1.1 rejection.

| Key | String | If refused |
|---|---|---|
| `NSPhotoLibraryUsageDescription` | "Geda Transfer reads the photos and videos you choose to send to your own computer." | The home screen explains what the library is read for and offers the prompt again. "Selected photos only" is treated as a normal answer, not an error |
| `NSPhotoLibraryAddUsageDescription` | "Geda Transfer saves files your computer sends to this phone." | A file that arrived is kept and retried rather than lost, with a message naming both ways out: allow it in Settings, or turn on "Save to Files instead" |
| `NSCameraUsageDescription` | "The camera is used once, to scan the pairing code your computer shows." | The pairing screen explains and offers the permission again |
| `NSLocalNetworkUsageDescription` | "Geda Transfer sends your photos directly to your computer over your own network. Nothing is uploaded to any service." | See below |

**The Local Network permission is the one that has to be handled carefully.**
iOS 14 and later gate every connection to a local-subnet address behind it, not
only Bonjour, and there is no API that reports its state. A user who declined
it gets an app that finds nothing, forever, with no error and no second prompt.

So the app recognises the shape of that failure: when every address it tried was
a local one and it has never once reached a local address, it says that iOS may
be blocking it and offers a jump to Settings
(`mobile/src/core/reachability.ts`). It is phrased as the likeliest cause rather
than a verdict, because a receiver that is switched off looks identical from
here. Addresses in the mesh-VPN ranges do not count as local: a connection
through a tunnel is exempt from the prompt, and blaming the permission for a
tunnel that is down would send the user to the wrong screen.

`NSBonjourServices` lists `_gedatransfer._tcp`, which is required for the mDNS
half of discovery to work at all.

There is no Face ID or microphone purpose string. Both are template defaults
from Expo's plugins for `expo-secure-store` and `expo-camera`; the app uses
neither, so both are switched off in `app.json` rather than shipped with copy
that describes something the app does not do.

## 8. Screenshots

Required: **6.9-inch iPhone** and, because the app supports iPad, **13-inch
iPad**. App Store Connect scales the rest. Confirm the current pixel sizes in
App Store Connect before uploading — Apple changes the required set roughly
once a year, and at the time of writing they are 1290 × 2796 (or 1320 × 2868)
and 2064 × 2752 (or 2048 × 2732).

The set, in order, is the story of a transfer:

| # | Screen | Shows |
|---|---|---|
| 1 | Home, one receiver paired, a selection made | What the app is for |
| 2 | Pairing | A QR code being scanned — this is the whole of setup |
| 3 | Transfer in progress | Throughput and the file list, because speed is the headline |
| 4 | Lock Screen with the Live Activity | That the phone can be put down |
| 5 | Inbox with files collected from the computer | That it works in both directions |
| 6 | Send options | Live Photos, ProRAW, edited versions — the format handling |

`scripts/screenshots.sh` captures them from a booted simulator with the app
installed, at the required sizes and named in this order. It needs an installed
iOS runtime and a build; it does not fake anything, and it fails rather than
producing a placeholder. Frames 3, 4, and 5 need a receiver running on the same
machine — start one with `go run .` from `cli/`.

No device frames, no invented UI, and no marketing text baked into the image
beyond a caption band: a screenshot that shows something the app does not do is
a rejection under 2.3.3 and, unlike most rejections, a deserved one.

## 9. App Review notes

The text to paste into App Review Notes. Keep it under a page; a reviewer reads
it once, standing up.

> Geda Transfer copies photos and files from the phone to a computer the user
> owns, over the local network. There is no account and no server, so there is
> nothing to give you a login for — but you do need a receiver on the same
> Wi-Fi as the review device. It takes about a minute:
>
> macOS: download <notarised build URL>, open it, and leave the window on the
> pairing screen. It shows a QR code.
>
> Or, with Docker: `docker compose -f docker/compose.yml up -d` then
> `docker compose -f docker/compose.yml exec gedad gedad pair`, which prints the
> QR code in the terminal.
>
> In the app: tap Pair, scan the code, then choose photos and Send. The files
> appear in the folder the receiver names on screen.
>
> Please allow the Local Network prompt on first launch. The app cannot work
> without it — it connects directly to the user's own computer, which iOS
> classes as a local network connection. If it is declined, the app explains
> this and offers a route to Settings.
>
> Background transfer: start a transfer and force-quit the app. It continues in
> a background URLSession and completes on its own. `processing` is declared
> only to schedule that hand-off when the phone is charging; the transfer itself
> is a background URLSession and needs no background mode.
>
> A video of the whole flow, including the parts that need two devices:
> <video URL>

## 10. Before you press submit

- [ ] `scripts/verify-p10.sh` is green.
- [ ] Version and build number bumped (`mobile/app.json`; EAS increments the
      build number for production).
- [ ] `eas build --profile production --platform ios` from `mobile/`.
- [ ] The screenshot set is current — a screenshot of an older UI is a
      2.3.3 rejection.
- [ ] The demo video is uploaded and the URL in the review notes resolves.
- [ ] The receiver build linked in the review notes is notarised and the link
      resolves from outside your network.
- [ ] Privacy policy URL resolves and matches [PRIVACY.md](PRIVACY.md).
- [ ] `eas submit --profile production --platform ios`.
- [ ] After upload, wait for the processing email. An ITMS-91053 reply means
      §4 is incomplete.
