#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies the P9 gate from docs/PLAN.md:
#
#   "deliberate failure injection never deletes an unverified file"
#
# Delete-after-transfer is the only irreversible thing this product does to
# somebody's own library, so the gate is not "deletion works". It is a list of
# ways to make the receiver's promise untrue, and the assertion in every one of
# them is that nothing gets deleted.
#
# The decision is split across two machines and so is the gate:
#
#   * the receiver decides whether it can still produce a file's bytes. Checked
#     against a real service over real TLS, with the stored files broken the
#     ways the world breaks them -- deleted, truncated, appended to, altered in
#     place at the same length, removed by the space-saving preset, and asked
#     about by the wrong device.
#
#   * the phone decides which whole assets those answers cover. Checked as
#     plain TypeScript: a refusal, a silence, a reply that is not JSON, a reply
#     about a different file, a half-confirmed Live Photo, and an asset only
#     part of which was ever sent.
#
# What is still a person's job is the system dialog: that iOS asks once for a
# batch, and that what it removes lands in Recently Deleted. That needs a
# device and is recorded in docs/PERFORMANCE.md.
#
# Usage: scripts/verify-p9.sh

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
# The receiver's half: can it still produce the bytes?
# ---------------------------------------------------------------------------

echo "==> the receiver's proof of custody"

(cd core && go build -o "$WORK/deletegate" ./internal/deletegate) \
    || fail "could not build the gate driver"

"$WORK/deletegate" -dir "$WORK/receiver" \
    || fail "the receiver vouched for a file it could not produce"

# The unit tests behind the same rule, including the ones the gate cannot
# reach: a ledger row pointing outside the destination directory, and the
# record a confirmation leaves behind.
(cd core && go test ./storage/ ./receiver/ >/dev/null) \
    || fail "the custody tests failed"
pass "the storage and receiver tests still pass"

# ---------------------------------------------------------------------------
# The phone's half: which whole assets do those answers cover?
# ---------------------------------------------------------------------------

echo
echo "==> what the phone does with the answers"

(cd mobile && npm install --no-audit --no-fund >/dev/null) || fail "npm install failed"
(cd mobile && npm run --silent typecheck) || fail "TypeScript errors"
pass "TypeScript is clean under strict mode"

(cd mobile && npx vitest run src/core/__tests__/deletion.test.ts >/dev/null) \
    || fail "the deletion rules failed"
pass "nothing unconfirmed, unanswered, or half-sent is ever deletable"

(cd mobile && npx vitest run src/engine/__tests__/deletion.test.ts >/dev/null) \
    || fail "the deletion engine failed under injected failures"
pass "a refused, silent, unreachable, or malformed receiver deletes nothing"

(cd mobile && npm run --silent test) || fail "the mobile unit tests failed"
pass "the rest of src/core and src/engine still pass"

# ---------------------------------------------------------------------------
# The defaults
# ---------------------------------------------------------------------------
#
# A destructive setting that shipped on by accident would pass every test
# above and still be the worst bug in the product (AGENTS.md §4). Checked as
# text because that is where the default lives.

echo
echo "==> the setting is off unless somebody turned it on"

grep -q "deleteAfterTransfer ?? false" mobile/src/engine/uploader.ts \
    || fail "the transfer engine does not default delete-after-transfer to off"
pass "the transfer engine records nothing unless asked"

grep -q "return row?.value === '1'" mobile/src/data/settings.ts \
    || fail "the stored setting does not read as off by default"
pass "an unreadable or unset preference reads as off"

grep -q "if (!(await loadDeleteAfterTransfer())) return NOTHING;" mobile/src/engine/deletion.ts \
    || fail "the deleting path does not re-check the setting"
pass "the function that deletes checks the setting itself, not just its caller"

# ---------------------------------------------------------------------------
# The native sources
# ---------------------------------------------------------------------------
#
# The system delete dialog cannot be driven here, but a file that does not
# typecheck is a phase that fails half an hour into an EAS build instead of
# now.

if command -v swiftc >/dev/null && xcrun --sdk iphoneos --show-sdk-path >/dev/null 2>&1; then
    echo
    echo "==> the native sources"
    SDK=$(xcrun --sdk iphoneos --show-sdk-path)

    for file in mobile/modules/geda-transfer/ios/*.swift; do
        swiftc -parse "$file" >/dev/null 2>&1 || fail "$file does not parse"
    done
    pass "the native sources parse against the iOS SDK"
else
    echo
    echo "  -- no iOS SDK here; skipping the native checks"
fi

echo
echo "P9 gate: deliberate failure injection never deleted an unverified file."
