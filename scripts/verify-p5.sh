#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies what can be verified of the P5 gate from docs/PLAN.md:
#
#   "app force-quit mid-transfer; transfer completes on its own and the result
#    is verified by hash"
#
# The gate has two halves and only one of them can be checked by a script.
#
#   * The protocol half -- an upload interrupted part way through is resumed
#     from the receiver's offset by a *second, independent* client, and the
#     stored file is byte-identical to the source. That is exactly what happens
#     when iOS relaunches the app and hands the remainder to a new task, and it
#     is checked here against a real receiver.
#   * The device half -- that `nsurlsessiond` keeps sending after the app is
#     swiped away, and that the app is relaunched to be told about it. No
#     simulator does this faithfully and no script can force-quit an app. It is
#     recorded in docs/PERFORMANCE.md by a person with a phone.
#
# Usage: scripts/verify-p5.sh

set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok: $*"; }

command -v node >/dev/null || fail "node is not installed"
command -v go >/dev/null || fail "go is not installed"

echo "==> installing the app's dependencies"
(cd mobile && npm install --no-audit --no-fund >/dev/null) || fail "npm install failed"

echo "==> typechecking"
(cd mobile && npm run --silent typecheck) || fail "TypeScript errors"
pass "TypeScript is clean under strict mode"

echo "==> testing the decision-making core"
(cd mobile && npm run --silent test) || fail "unit tests failed"
pass "src/core and src/engine tests pass"

# ---------------------------------------------------------------------------
# The native sources
# ---------------------------------------------------------------------------

if command -v swiftc >/dev/null && xcrun --sdk iphoneos --show-sdk-path >/dev/null 2>&1; then
    echo "==> checking the native module"
    SDK=$(xcrun --sdk iphoneos --show-sdk-path)

    for file in mobile/modules/geda-transfer/ios/*.swift mobile/ios-extensions/GedaLiveActivity/*.swift; do
        swiftc -parse "$file" >/dev/null 2>&1 || fail "$file does not parse"
    done
    pass "every Swift source parses"

    # These import only system frameworks, so they typecheck fully against the
    # iOS SDK. The module and the app-delegate subscriber need ExpoModulesCore
    # and are only parsed above; they are compiled by the real build.
    (cd mobile/modules/geda-transfer/ios \
        && swiftc -typecheck -sdk "$SDK" -target arm64-apple-ios16.4 \
             SPKIPin.swift PinnedClient.swift Tus.swift BackgroundStore.swift \
             BackgroundUploader.swift LiveActivity.swift GedaTransferAttributes.swift \
             >/dev/null) \
        || fail "the background transfer sources do not typecheck"
    pass "the background session and the pinning typecheck against the iOS SDK"

    (swiftc -typecheck -sdk "$SDK" -target arm64-apple-ios16.4 \
        mobile/ios-extensions/GedaLiveActivity/GedaLiveActivity.swift \
        mobile/modules/geda-transfer/ios/GedaTransferAttributes.swift >/dev/null) \
        || fail "the Live Activity widget does not typecheck"
    pass "the Live Activity widget typechecks"
else
    echo "  -- no iOS SDK here; skipping the native checks"
fi

# ---------------------------------------------------------------------------
# The generated project
# ---------------------------------------------------------------------------
#
# ios/ is generated and not checked in, so the widget extension exists only as
# a config plugin. A plugin that silently stops adding the target is a Live
# Activity that silently never appears.

echo "==> generating the Xcode project"
(cd mobile && npx expo prebuild --platform ios --clean --no-install >/dev/null 2>&1) \
    || fail "expo prebuild failed"

PROJECT=mobile/ios/GedaTransfer.xcodeproj/project.pbxproj
PLIST=mobile/ios/GedaTransfer/Info.plist

grep -q "GedaLiveActivity.appex in Embed App Extensions" "$PROJECT" \
    || fail "the widget extension is not embedded in the app"
grep -q "PBXTargetDependency" "$PROJECT" \
    || fail "the app does not depend on the widget extension, so it may be embedded stale"
pass "the Live Activity extension is a target, embedded in the app"

for key in NSSupportsLiveActivities UIBackgroundModes BGTaskSchedulerPermittedIdentifiers; do
    grep -q "$key" "$PLIST" || fail "$key is missing from the app's Info.plist"
done
grep -q "app.geda.transfer.kickoff" "$PLIST" \
    || fail "the background kickoff identifier is not permitted in Info.plist"
pass "background processing and Live Activities are declared"

# The identifier the system uses to hand the app back its finished uploads.
# Changing it orphans every transfer that was in flight during the update.
grep -q '"app.geda.transfer.upload"' mobile/modules/geda-transfer/ios/BackgroundUploader.swift \
    || fail "the background session identifier changed; in-flight uploads would be orphaned"
pass "the background session identifier is unchanged"

# ---------------------------------------------------------------------------
# The gate: resume by a different client, verified by hash
# ---------------------------------------------------------------------------

echo "==> interrupted upload, resumed by a second client, verified by hash"

STATE=$(mktemp -d)
PORT=$(( 40000 + RANDOM % 20000 ))
BASE="https://127.0.0.1:$PORT"
TOKEN="dev-token"
CURL=(curl --silent --show-error --insecure)

cleanup() {
    if [ -n "${SERVER_PID:-}" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    rm -rf "$STATE"
}
trap cleanup EXIT

b64() { printf '%s' "$1" | base64 | tr -d '\n'; }
sha() { shasum -a 256 "$1" 2>/dev/null | awk '{print $1}' || sha256sum "$1" | awk '{print $1}'; }

(cd core && go build -o "$STATE/devserver" ./internal/devserver) || fail "could not build the receiver"
"$STATE/devserver" -dir "$STATE" -addr "127.0.0.1:$PORT" -token "$TOKEN" \
    >"$STATE/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 100); do
    "${CURL[@]}" --fail "$BASE/v1/info" >/dev/null 2>&1 && break
    sleep 0.2
done
"${CURL[@]}" --fail "$BASE/v1/info" >/dev/null \
    || fail "receiver did not come up; log: $(cat "$STATE/server.log")"

SRC="$STATE/video.bin"
head -c 8388608 /dev/urandom > "$SRC"
SIZE=$(wc -c < "$SRC" | tr -d ' ')

META="filename $(b64 'VID_0001.MOV'),captured_at $(b64 '2026-07-04T15:09:03Z'),kind $(b64 video)"
LOCATION=$("${CURL[@]}" -D - -o /dev/null -X POST "$BASE/v1/files/" \
    -H "Authorization: Bearer $TOKEN" -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Length: $SIZE" -H "Upload-Metadata: $META" \
    | tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')
[ -n "$LOCATION" ] || fail "the receiver returned no upload location"

# Send part of it and stop, as a phone does when it is force-quit: the tus URL
# is all that survives, which is why the app records it before a byte moves.
CUT=3000000
head -c "$CUT" "$SRC" > "$STATE/part1.bin"
"${CURL[@]}" -o /dev/null -X PATCH "$LOCATION" \
    -H "Authorization: Bearer $TOKEN" -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Offset: 0" -H "Content-Type: application/offset+octet-stream" \
    --data-binary "@$STATE/part1.bin" || true

# A *new* client, sharing nothing with the first but the URL -- the situation
# after a relaunch, where the only state is what was written to disk.
OFFSET=$("${CURL[@]}" -D - -o /dev/null --head "$LOCATION" \
    -H "Authorization: Bearer $TOKEN" -H "Tus-Resumable: 1.0.0" \
    | tr -d '\r' | awk '/^[Uu]pload-[Oo]ffset:/ {print $2}')
[ "$OFFSET" = "$CUT" ] || fail "resume offset is '$OFFSET', expected $CUT"
pass "the receiver reports offset $OFFSET after the interruption"

# The app writes the remainder to its own file before handing it over, because
# a background URLSession can send a file and nothing else. Same operation.
tail -c "+$((CUT + 1))" "$SRC" > "$STATE/remainder.bin"
STORED=$("${CURL[@]}" -D - -o /dev/null -X PATCH "$LOCATION" \
    -H "Authorization: Bearer $TOKEN" -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Offset: $OFFSET" -H "Content-Type: application/offset+octet-stream" \
    --data-binary "@$STATE/remainder.bin" \
    | tr -d '\r' | awk '/^[Gg]eda-[Ss]tored-[Pp]ath:/ {print $2}' | base64 -d)
[ -n "$STORED" ] || fail "the resumed upload did not complete"

DEST="$STATE/Photos/$STORED"
[ -f "$DEST" ] || fail "the resumed file is not at $DEST"
[ "$(sha "$SRC")" = "$(sha "$DEST")" ] \
    || fail "the resumed file's hash differs from the source"
pass "resumed from a cold client and the hashes match: $(sha "$SRC" | cut -c1-16)…"

# ---------------------------------------------------------------------------

echo
cat <<'MESSAGE'
P5 protocol gate: PASS

The device half of the gate is not automatable and is not claimed here:

  1. cd mobile && npx eas build --profile development --platform ios
  2. pair the phone, choose "Send in the background", then swipe the app away
  3. leave the phone on Wi-Fi; check the Lock Screen activity
  4. reopen the app and confirm every file is marked as sent
  5. on the receiver, compare hashes against the phone's originals
  6. record the run in docs/PERFORMANCE.md

Until that row exists, P5's gate is met in protocol and unverified on a device.
MESSAGE
