#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies what can be verified of the P7 gate from docs/PLAN.md:
#
#   "a 2GB ZIP lands in Files; a video lands in Photos, both verified"
#
# The gate has two halves and, as with P4 and P5, only one of them is a
# property of this repository.
#
#   * The protocol half -- a 2 GiB file queued on a receiver, collected by a
#     client that shares nothing with it but a pinned key, interrupted part way
#     through, resumed by range, and verified against the digest the receiver
#     published. That runs here, against a real server over real TLS.
#   * The device half -- that the ZIP appears in the Files app and the video in
#     the camera roll. That needs an iPhone and a person looking at it, and it
#     is recorded in docs/PERFORMANCE.md.
#
# What the script does assert about the phone is the decision it makes: which
# destination each kind of file is allowed to reach. That is plain TypeScript
# with no device in it, on purpose, and it is where "a ZIP must never be
# offered to the photo library" is enforced.
#
# Usage: scripts/verify-p7.sh [--size BYTES]
#
#   --size  how big the archive is. Defaults to 2 GiB, which is the gate.
#           Smaller is for a laptop with no disk to spare; it does not count.

set -euo pipefail
cd "$(dirname "$0")/.."

SIZE=$((2 * 1024 * 1024 * 1024))
while [ $# -gt 0 ]; do
    case "$1" in
        --size) SIZE=$2; shift 2 ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok: $*"; }

command -v go >/dev/null || fail "go is not installed"
command -v node >/dev/null || fail "node is not installed"

WORK=$(mktemp -d)
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# The rule about where a file may land
# ---------------------------------------------------------------------------
#
# This is the half of the gate's sentence that is a decision rather than a
# transfer. It lives in src/core with no React Native imports, so it is checked
# here rather than on a phone.

echo "==> the phone's decision-making"

(cd mobile && npm install --no-audit --no-fund >/dev/null) || fail "npm install failed"
(cd mobile && npm run --silent typecheck) || fail "TypeScript errors"
pass "TypeScript is clean under strict mode"

(cd mobile && npx vitest run src/core/__tests__/inbox.test.ts src/engine/__tests__/inbox.test.ts >/dev/null) \
    || fail "the routing and collection tests failed"
pass "a ZIP can only reach Files; media reaches Photos unless the Advanced setting says otherwise"

(cd mobile && npm run --silent test) || fail "the mobile unit tests failed"
pass "the rest of src/core and src/engine still pass"

# ---------------------------------------------------------------------------
# The native sources
# ---------------------------------------------------------------------------
#
# The download session cannot be run here -- it belongs to nsurlsessiond -- but
# a session that does not compile is a phase that fails half an hour into an
# EAS build instead of now.

if command -v swiftc >/dev/null && xcrun --sdk iphoneos --show-sdk-path >/dev/null 2>&1; then
    echo "==> the download session"
    SDK=$(xcrun --sdk iphoneos --show-sdk-path)

    for file in mobile/modules/geda-transfer/ios/*.swift; do
        swiftc -parse "$file" >/dev/null 2>&1 || fail "$file does not parse"
    done

    (cd mobile/modules/geda-transfer/ios \
        && swiftc -typecheck -sdk "$SDK" -target arm64-apple-ios16.4 \
             SPKIPin.swift PinnedClient.swift Tus.swift BackgroundStore.swift \
             BackgroundUploader.swift LiveActivity.swift GedaTransferAttributes.swift \
             DownloadStore.swift BackgroundDownloader.swift \
             >/dev/null) \
        || fail "the background download sources do not typecheck"
    pass "the download session and its store typecheck against the iOS SDK"

    # Changing either identifier orphans every transfer that was in flight
    # during an update: the system quotes it when it relaunches the app, and
    # nothing else can claim those tasks.
    grep -q '"app.geda.transfer.download"' mobile/modules/geda-transfer/ios/BackgroundDownloader.swift \
        || fail "the download session identifier changed; in-flight downloads would be orphaned"
    grep -q '"app.geda.transfer.upload"' mobile/modules/geda-transfer/ios/BackgroundUploader.swift \
        || fail "the upload session identifier changed; in-flight uploads would be orphaned"
    pass "both background session identifiers are unchanged"
else
    echo "  -- no iOS SDK here; skipping the native checks"
fi

# ---------------------------------------------------------------------------
# The protocol half of the gate
# ---------------------------------------------------------------------------

echo "==> $(( SIZE / 1024 / 1024 )) MiB queued, collected, interrupted, resumed, verified"

FREE_KB=$(df -Pk "$WORK" | awk 'NR==2 {print $4}')
NEEDED_KB=$(( SIZE / 1024 * 2 ))
if [ "$FREE_KB" -lt "$NEEDED_KB" ]; then
    fail "not enough free space for a $(( SIZE / 1024 / 1024 )) MiB run: $(( FREE_KB / 1024 )) MiB free, $(( NEEDED_KB / 1024 )) MiB needed"
fi

(cd core && go build -o "$WORK/outboxgate" ./internal/outboxgate) \
    || fail "could not build the gate driver"

"$WORK/outboxgate" -dir "$WORK" -size "$SIZE" || fail "the gate did not pass"

# ---------------------------------------------------------------------------

echo
cat <<'MESSAGE'
P7 protocol gate: PASS

The device half is not automatable and is not claimed here:

  1. cd mobile && npx eas build --profile development --platform ios
  2. on the desktop, pick the phone and "Send files": a 2 GB ZIP and a video
  3. open the app on the phone, then close it and wait
  4. open it again, and check both:
       * the ZIP in Files › On My iPhone › Geda Transfer › Received
       * the video in Photos, dated when it was shot rather than today
  5. record the run in docs/PERFORMANCE.md, including one where it went wrong

Until that table has a row, P7's gate is met in everything the protocol does
and unverified in the two places the sentence actually names.
MESSAGE
