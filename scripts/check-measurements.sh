#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Which gate measurements have actually been recorded in docs/PERFORMANCE.md.
#
# Five gates end in something only a person holding hardware can produce:
# P4's MB/s from a physical iPhone, P5's force-quit run, P6's session with
# somebody who has never seen the app, P7's look in the two places on a phone
# where the files are supposed to be, and P8's proof that a real Live Photo and
# a real ProRAW shot are what the receiver has been tested against. None of
# them can be produced by a machine, so none of them can be produced by CI.
#
# This script reports which of those rows exist. It exits 0 by default, so the
# phase scripts can check everything a script *can* check on a machine with no
# iPhone in it. `--strict` turns a missing row into an error, which is what to
# use when claiming a phase is done.
#
# Usage: scripts/check-measurements.sh [--strict] [P4 ...]

set -euo pipefail
cd "$(dirname "$0")/.."

STRICT=0
if [ "${1:-}" = "--strict" ]; then
    STRICT=1
    shift
fi
GATES=("$@")
[ ${#GATES[@]} -gt 0 ] || GATES=(P4 P5 P6 P7 P8)

RESULTS=docs/PERFORMANCE.md
[ -f "$RESULTS" ] || { echo "FAIL: $RESULTS is missing" >&2; exit 1; }

# The recorded rows under one gate's heading. A row that is not the
# placeholder starts with a date, which is why the placeholder does not.
rows_for() {
    awk -v want="^## $1 gate" '
        /^## P[0-9]+ gate/ { inside = ($0 ~ want); next }
        inside && /^\| 20[0-9][0-9]-[0-9][0-9]-[0-9][0-9] \|/ { print }
    ' "$RESULTS"
}

missing=()
for gate in "${GATES[@]}"; do
    rows=$(rows_for "$gate")
    if [ -z "$rows" ]; then
        missing+=("$gate")
        echo "  --: $gate has no recorded run"
        continue
    fi

    latest=$(printf '%s\n' "$rows" | tail -1)
    count=$(printf '%s\n' "$rows" | wc -l | tr -d ' ')
    date=$(printf '%s' "$latest" | awk -F'|' '{gsub(/ /,"",$2); print $2}')

    if [ "$gate" = P4 ]; then
        # The baseline everything after P4 is compared against: the transfer
        # rate column of the most recent run.
        rate=$(printf '%s' "$latest" | awk -F'|' '{gsub(/ /,"",$12); print $12}')
        [ -n "$rate" ] || { echo "FAIL: the last P4 row has no transfer rate" >&2; exit 1; }
        echo "  ok: $gate recorded, $count run(s), latest $date — baseline ${rate} MB/s transfer"
    else
        echo "  ok: $gate recorded, $count run(s), latest $date"
    fi
done

[ ${#missing[@]} -gt 0 ] || exit 0

if [ "$STRICT" = 1 ]; then
    cat >&2 <<MESSAGE

FAIL: unmeasured on a device: ${missing[*]}

These gates are numbers and observations from real hardware. $RESULTS has the
steps for each one; paste the row it produces into the table there.

An unmeasured gate is not a passed one.
MESSAGE
    exit 1
fi

cat <<MESSAGE

Unmeasured on a device: ${missing[*]}. What a script can check is checked; the
runs that need hardware are not, and are not claimed here. See $RESULTS.
MESSAGE
