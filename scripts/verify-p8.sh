#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies the P8 gate from docs/PLAN.md:
#
#   "Live Photo round-trips as a linked pair; ProRAW keeps its DNG"
#
# Unlike P4, P5, and P7, both halves of this sentence are properties of this
# repository rather than of a phone, so both are checked here for real: a
# receiver over real TLS, a real pairing, a HEIC and a MOV uploaded as one
# pair, and a DNG beside them -- under every output preset, including the one
# whose whole purpose is to delete originals.
#
# The run happens twice, and the second time is the point:
#
#   * with no converter installed, which is what a fresh machine is. Nothing
#     is converted and everything still arrives.
#   * with a converter installed, which is the only configuration in which
#     "space-saving did not delete half of a Live Photo" means anything.
#
# What is still a person's job is the phone: that a Live Photo taken on an
# iPhone comes back as a Live Photo, and that a ProRAW shot exported by iOS is
# the same DNG. That needs a device and is recorded in docs/PERFORMANCE.md.
#
# Usage: scripts/verify-p8.sh

set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok: $*"; }

command -v go >/dev/null || fail "go is not installed"
command -v node >/dev/null || fail "node is not installed"

WORK=$(mktemp -d)
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# The phone's half of the decision
# ---------------------------------------------------------------------------
#
# Which resources of an asset leave the phone is plain TypeScript with no
# device in it, on purpose (src/core/selection.ts). It is where "a ProRAW shot
# keeps its negative" and "a Live Photo keeps its motion" are decided.

echo "==> what the phone chooses to send"

(cd mobile && npm install --no-audit --no-fund >/dev/null) || fail "npm install failed"
(cd mobile && npm run --silent typecheck) || fail "TypeScript errors"
pass "TypeScript is clean under strict mode"

(cd mobile && npx vitest run src/core/__tests__/selection.test.ts >/dev/null) \
    || fail "the selection tests failed"
pass "Live Photos keep their motion, ProRAW keeps its negative, no asset resolves to nothing"

(cd mobile && npm run --silent test) || fail "the mobile unit tests failed"
pass "the rest of src/core and src/engine still pass"

# ---------------------------------------------------------------------------
# The native sources
# ---------------------------------------------------------------------------
#
# Reading PHAssetResource cannot be run here -- it needs a photo library -- but
# a file that does not typecheck is a phase that fails half an hour into an EAS
# build instead of now.

if command -v swiftc >/dev/null && xcrun --sdk iphoneos --show-sdk-path >/dev/null 2>&1; then
    echo "==> reading the photo library"
    SDK=$(xcrun --sdk iphoneos --show-sdk-path)

    for file in mobile/modules/geda-transfer/ios/*.swift; do
        swiftc -parse "$file" >/dev/null 2>&1 || fail "$file does not parse"
    done

    (cd mobile/modules/geda-transfer/ios \
        && swiftc -typecheck -sdk "$SDK" -target arm64-apple-ios16.4 \
             SPKIPin.swift PinnedClient.swift Tus.swift BackgroundStore.swift \
             BackgroundUploader.swift LiveActivity.swift GedaTransferAttributes.swift \
             DownloadStore.swift BackgroundDownloader.swift AssetLibrary.swift \
             >/dev/null) \
        || fail "the photo library sources do not typecheck"
    pass "the resource reader typechecks against the iOS SDK"
else
    echo "  -- no iOS SDK here; skipping the native checks"
fi

# ---------------------------------------------------------------------------
# The gate itself
# ---------------------------------------------------------------------------

(cd core && go build -o "$WORK/formatsgate" ./internal/formatsgate) \
    || fail "could not build the gate driver"

# A receiver on a machine with nothing installed. This is what most people
# have, and it must receive exactly as well as a machine with ffmpeg.
echo
echo "==> with no converter installed"
# The overrides point at nothing, which is how a machine with no converter is
# simulated on a developer's laptop that has one. An override is honoured
# exactly as given, including when it does not exist -- silently falling back
# to a different binary is what this run is checking does not happen.
GEDA_FFMPEG="$WORK/not-installed" \
GEDA_FFPROBE="$WORK/not-installed" \
GEDA_HEIF_CONVERT="$WORK/not-installed" \
    "$WORK/formatsgate" -dir "$WORK/bare" \
    || fail "the gate did not pass on a machine with no converter"

# And with one. A gate that only ran without a converter would never execute
# the code that deletes an original, which is the code worth gating.
#
# The stand-in rather than a real ffmpeg: the point here is what the receiver
# does with the result -- which file it writes, which original it keeps -- and
# that must be checked on every machine, including CI runners with no ffmpeg.
# Whether libx264 produces a good picture is not this repository's question.
(cd core && go build -o "$WORK/faketool" ./internal/faketool) \
    || fail "could not build the stand-in converter"

echo
echo "==> with a converter installed"
GEDA_FAKE_TOOL_MODE=ok \
GEDA_FAKE_TOOL_CODEC=hevc \
GEDA_FFMPEG="$WORK/faketool" \
GEDA_FFPROBE="$WORK/faketool" \
GEDA_HEIF_CONVERT="$WORK/faketool" \
    "$WORK/formatsgate" -dir "$WORK/converted" \
    || fail "the gate did not pass with a converter installed"

# ---------------------------------------------------------------------------
# What this machine would actually use
# ---------------------------------------------------------------------------
#
# Reported, never asserted. A missing ffmpeg is not a failing build: it is a
# receiver that keeps originals, which is the default anyway.

echo
echo "==> the converters on this machine"
for tool in ffmpeg ffprobe heif-convert; do
    if path=$(command -v "$tool" 2>/dev/null); then
        echo "  -- $tool: $path"
    else
        echo "  -- $tool: not installed (nothing would be converted here)"
    fi
done

# ---------------------------------------------------------------------------

echo
cat <<'MESSAGE'
P8 gate: PASS

Both halves hold against a real receiver, under every preset, with and without
a converter installed:

  * a Live Photo's HEIC and MOV share one basename and differ only by extension
  * a ProRAW DNG comes back byte for byte, under its own extension, with no
    conversion queued against it -- not by a preset, not by a matrix, and not
    by a ledger row nothing validated

What is not claimed here is the phone. Do this once on a device:

  1. cd mobile && npx eas build --profile development --platform ios
  2. take a Live Photo and a ProRAW shot on the phone
  3. send both, then look at the destination folder on the computer:
       * IMG_xxxx.HEIC and IMG_xxxx.MOV, same name, side by side
       * IMG_yyyy.DNG, opening in Photoshop or Lightroom as a raw negative
  4. turn on "Also save a copy anything can open" and send again: a .jpg
     appears beside the HEIC and the HEIC is still there
  5. record the run in docs/PERFORMANCE.md

Until that table has a row, P8's gate is met in everything this repository
decides and unverified on the one device that takes the photographs.
MESSAGE
