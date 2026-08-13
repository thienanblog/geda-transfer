#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies what can be verified of the P6 gate from docs/PLAN.md:
#
#   "a person who has never seen the app can pair and transfer without
#    instructions"
#
# That gate is a person. No script asserts it, and this one does not claim to.
#
# What it does assert is everything that person depends on, because each of
# these failing is a way the gate fails for reasons that have nothing to do
# with the design:
#
#   * core/ still has no UI dependency, and the desktop is still a layer over
#     it rather than a second implementation (AGENTS.md §2);
#   * the app compiles, its logic is tested, and its page typechecks;
#   * a machine with nothing configured comes up ready to receive;
#   * the first screen's pairing code is one a real pinned TLS client redeems,
#     and a file sent against it arrives, verifies, and is findable;
#   * the live view saw it happen;
#   * the settings a first-time user changes take effect, and the ones that
#     cannot work are refused.
#
# The last four run against the app's own bindings -- the same calls the window
# makes -- so a regression behind the window fails here rather than in front of
# somebody being handed a laptop.
#
# Usage: scripts/verify-p6.sh

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
# The layering rule
# ---------------------------------------------------------------------------
#
# core/ is the single source of truth and must not import a UI package. The
# desktop is allowed to import core; the reverse is what this checks.

echo "==> the layering rule"

if (cd core && go list -deps ./... 2>/dev/null | grep -qE '^github\.com/(wailsapp|energye)/'); then
    fail "core/ imports a UI package; it must stay free of one (AGENTS.md §2)"
fi
pass "core/ pulls in no UI package"

# The desktop must run *core's* receiver rather than assembling its own. If it
# ever stops importing core/service, the two ends of the product have forked.
(cd desktop && go list -deps ./internal/app | grep -q 'geda-transfer/core/service') \
    || fail "the desktop app does not use core/service; it has its own receiver"
pass "the desktop runs core/service, the same receiver gedad runs"

# ---------------------------------------------------------------------------
# It builds, and its logic is tested
# ---------------------------------------------------------------------------

echo "==> building"

for module in core cli desktop; do
    unformatted=$(cd "$module" && gofmt -l .)
    [ -z "$unformatted" ] || fail "$module/ is not gofmt'd: $unformatted"
done
pass "every module is gofmt'd"

(cd desktop && go vet ./... >/dev/null) || fail "go vet found problems in desktop/"
(cd desktop && go build ./... >/dev/null) || fail "desktop/ does not build"
pass "desktop/ builds and vets clean"

# The window is macOS and Windows. A desktop that only compiles on the machine
# it was written on is not a shipped desktop.
for target in darwin/arm64 darwin/amd64 windows/amd64 windows/arm64; do
    (cd desktop && GOOS=${target%/*} GOARCH=${target#*/} go build ./internal/... >/dev/null 2>&1) \
        || fail "desktop/internal does not build for $target"
done
pass "the platform-specific code builds for macOS and Windows"

echo "==> testing"
(cd desktop && go test -count=1 ./... >/dev/null) || fail "desktop/ tests failed"
pass "the live view, the settings, and the pairing code are tested"

echo "==> typechecking the window"
(cd desktop/frontend && npm ci --no-audit --no-fund >/dev/null 2>&1) \
    || fail "the window's dependencies would not install"
(cd desktop/frontend && npm run --silent typecheck) || fail "TypeScript errors"
pass "the page typechecks under strict mode"

(cd desktop/frontend && npm run --silent build >/dev/null) || fail "the page does not build"
pass "the page builds"

# ---------------------------------------------------------------------------
# The gate itself, driven through the app's own bindings
# ---------------------------------------------------------------------------

echo "==> pairing and transferring, through the bindings the window calls"

STATE="$WORK/state"
DEST="$WORK/Received"
mkdir -p "$STATE" "$DEST"

# tusd logs every request through its own logger, which is noise here. Its
# stderr is kept in a file so a failure can still be explained.
if ! (cd desktop && go run ./internal/gate -state "$STATE" -dest "$DEST" 2>"$WORK/gate.log"); then
    echo "--- the app's log ---" >&2
    tail -40 "$WORK/gate.log" >&2
    fail "the gate did not pass"
fi

# ---------------------------------------------------------------------------

echo
cat <<'MESSAGE'
P6 gate: PASS, as far as a script can take it.

What is left is the half that is a person, and it needs one who has not seen
the app before:

  1. cd desktop && wails build
  2. hand them the built app and a phone with Geda Transfer on it
  3. say nothing
  4. watch: do they pair, send a photo, and find it on disk?
  5. record what they got stuck on in docs/PERFORMANCE.md

Until that row exists, P6's gate is met in everything the app does and
unverified in the one thing it is actually about.
MESSAGE
