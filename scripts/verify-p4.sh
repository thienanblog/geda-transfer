#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies the P4 gate from docs/PLAN.md: measured MB/s on 200 mixed photos and
# one 4K video, recorded in the repository.
#
# This script checks the code around that number -- types, the tested core, the
# native sources -- and then reports whether the number itself has been
# recorded. The number cannot be automated: it has to come from a physical
# iPhone on a real network. A simulator has no PHAsset export cost and no
# radio, so a figure from one would be a fiction that every later phase is
# compared against.
#
# So a missing measurement is reported, not asserted, exactly as in P5 and P6.
# It is not something a change to this repository can break, and a check that
# no commit can turn green is one nobody reads.
#
# Usage: scripts/verify-p4.sh [--require-measurement]

set -euo pipefail
cd "$(dirname "$0")/.."

# --require-measurement: fail unless docs/PERFORMANCE.md has a real run in it.
REQUIRE=""
[ "${1:-}" = "--require-measurement" ] && REQUIRE="--strict"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok: $*"; }

command -v node >/dev/null || fail "node is not installed"

echo "==> installing the app's dependencies"
(cd mobile && npm install --no-audit --no-fund >/dev/null) || fail "npm install failed"

echo "==> typechecking"
(cd mobile && npm run --silent typecheck) || fail "TypeScript errors"
pass "TypeScript is clean under strict mode"

echo "==> testing the decision-making core"
(cd mobile && npm run --silent test) || fail "unit tests failed"
pass "src/core tests pass"

# The native module cannot be exercised from Node, and building it needs Xcode
# with the iOS platform installed. What can be checked anywhere with a Swift
# toolchain is worth checking: the pinning code compiles, and -- the part that
# matters -- it computes the same pin the receiver reports.
# `swiftc` alone is not enough: the Linux toolchain has it and has no Apple
# SDK, so the check below would fail on `import CryptoKit` and report a
# missing platform as a broken pin implementation.
if command -v swiftc >/dev/null && xcrun --sdk macosx --show-sdk-path >/dev/null 2>&1; then
    echo "==> checking the native module"
    WORK=$(mktemp -d)
    trap 'rm -rf "$WORK"' EXIT

    for file in mobile/modules/geda-transfer/ios/*.swift; do
        swiftc -parse "$file" >/dev/null 2>&1 || fail "$file does not parse"
    done

    # These two import only Foundation, Security, and CryptoKit, so they can be
    # fully typechecked against the macOS SDK. The module file needs
    # ExpoModulesCore and is only parsed above.
    (cd mobile/modules/geda-transfer/ios \
        && swiftc -typecheck -sdk "$(xcrun --sdk macosx --show-sdk-path)" \
             SPKIPin.swift PinnedClient.swift >/dev/null) \
        || fail "the pinning and transport sources do not typecheck"
    pass "Swift sources parse and typecheck"

    if command -v go >/dev/null; then
        echo "==> the phone and the receiver must agree on the pin"
        # Two implementations in two languages -- a DER walk in Swift,
        # crypto/x509 in Go. If they ever disagree, every pairing fails with a
        # mismatch that has no override, so this is checked rather than assumed.
        (cd mobile/modules/geda-transfer/ios \
            && swiftc -O SPKIPin.swift checks/main.swift -o "$WORK/pincheck" 2>/dev/null) \
            || fail "could not build the pin checker"
        (cd cli && go build -o "$WORK/gedad" .) || fail "could not build gedad"

        "$WORK/gedad" run -state-dir "$WORK/state" -listen 127.0.0.1:0 \
            -set discovery=false >"$WORK/gedad.log" 2>&1 &
        gedad_pid=$!

        for _ in $(seq 1 50); do
            [ -f "$WORK/state/identity/identity.crt" ] && break
            sleep 0.2
        done

        reported=$("$WORK/gedad" status -state-dir "$WORK/state" -json 2>/dev/null \
            | sed -n 's/.*"spki": *"\([^"]*\)".*/\1/p')
        computed=$("$WORK/pincheck" "$WORK/state/identity/identity.crt" 2>/dev/null || true)

        kill "$gedad_pid" 2>/dev/null || true
        wait "$gedad_pid" 2>/dev/null || true

        [ -n "$reported" ] || fail "the receiver reported no pin; log: $(cat "$WORK/gedad.log")"
        [ "$reported" = "$computed" ] \
            || fail "the app would compute '$computed' for a certificate the receiver pins as '$reported'"
        pass "both sides compute the same SPKI pin: $reported"
    fi
else
    echo "  -- no Apple Swift toolchain here; skipping the native checks"
fi

echo "==> checking the recorded measurement"
# The gate itself is a number from a physical iPhone, so this reports rather
# than asserts -- the same shape as P5 and P6, whose device halves are also
# recorded in docs/PERFORMANCE.md rather than claimed by a script. Pass
# --require-measurement to make a missing row an error, which is what to use
# when claiming the phase is done.
./scripts/check-measurements.sh $REQUIRE P4

echo
if [ -n "$REQUIRE" ]; then
    echo "P4 gate: PASS"
else
    cat <<'MESSAGE'
P4 gate: PASS, as far as a script can take it.

The measurement itself is not automatable and is not claimed here:

  1. docker compose -f docker/compose.yml up -d
  2. cd mobile && npx eas build --profile development --platform ios
  3. gedad pair, and scan the code with the app
  4. in the app: Run the benchmark
  5. paste the row it produces into the table in docs/PERFORMANCE.md

Until that row exists, P4's gate is met in code and unmeasured on a device.
MESSAGE
fi
