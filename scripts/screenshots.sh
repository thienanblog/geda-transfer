#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Captures the App Store screenshot set (docs/APPSTORE.md §8).
#
# Six frames on each of two devices, at the sizes App Store Connect requires,
# named and ordered so that uploading them is drag-and-drop.
#
# This is deliberately an *assisted* capture rather than an automatic one.
# Driving the app to each screen would need a UI test target, and the two most
# valuable frames -- a Live Activity on the Lock Screen, and a transfer at full
# speed -- are not states a script can conjure anyway: they need a receiver on
# the network and files actually moving. So the operator navigates and this
# script does the parts that are easy to get wrong: the right device, the right
# size, the right name, the right directory, and a check that what came out is
# the size the store will accept.
#
# It captures nothing it cannot capture honestly: no placeholder, no mock-up,
# no resized frame from another device. A screenshot of something the app does
# not do is a 2.3.3 rejection and a deserved one.
#
# Before running:
#   * an iOS runtime installed (Xcode > Settings > Components)
#   * the app built and installed on the simulator:
#         cd mobile && npx expo run:ios --configuration Release
#   * a receiver running for frames 3, 4, and 5:
#         cd cli && go run .
#
# Usage: scripts/screenshots.sh [--iphone <device>] [--ipad <device>]
#                               [--only iphone|ipad] [--frame <name>]

set -euo pipefail
cd "$(dirname "$0")/.."

BUNDLE_ID=app.geda.transfer
OUT=docs/appstore/screenshots

# Names change with each year's hardware; both are overridable because the
# store's size classes outlive the device that first had them.
IPHONE="iPhone 17 Pro Max"
IPAD="iPad Pro 13-inch (M5)"
ONLY=""
ONE_FRAME=""

while [ $# -gt 0 ]; do
    case "$1" in
        --iphone) IPHONE="$2"; shift 2 ;;
        --ipad) IPAD="$2"; shift 2 ;;
        --only) ONLY="$2"; shift 2 ;;
        --frame) ONE_FRAME="$2"; shift 2 ;;
        -h|--help) sed -n '4,32p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

fail() { echo "FAIL: $*" >&2; exit 1; }

command -v xcrun >/dev/null || fail "xcrun is not available; this needs a Mac with Xcode"

# The frames, in the order they appear in the listing, with what each has to
# show. The prompts are the shot list -- a screenshot of an empty state helps
# nobody decide whether to install the app.
FRAMES=(
    "01-home|Home, with one receiver paired and a selection made"
    "02-pairing|The pairing screen, camera on a QR code"
    "03-transfer|A transfer running: throughput visible, files in flight"
    "04-live-activity|The Lock Screen, Live Activity showing progress"
    "05-inbox|The inbox, with files collected from the computer"
    "06-send-options|Send options: Live Photos, ProRAW, edited versions"
)

# What App Store Connect accepts, portrait, as of this writing. Confirm against
# the current specification before an upload: Apple revises the required set
# roughly once a year, and a wrong size is refused at upload rather than in
# review.
sizes_for() {
    case "$1" in
        iphone-6.9) echo "1320x2868 1290x2796" ;;
        ipad-13) echo "2064x2752 2048x2732" ;;
    esac
}

capture_device() {
    local label="$1" device="$2"
    local udid

    udid=$(xcrun simctl list devices available -j \
        | python3 -c "
import json, sys
name = sys.argv[1]
data = json.load(sys.stdin)['devices']
for runtime, devices in data.items():
    if 'iOS' not in runtime:
        continue
    for device in devices:
        if device['name'] == name and device.get('isAvailable'):
            print(device['udid'])
            raise SystemExit
" "$device")

    if [ -z "$udid" ]; then
        echo >&2
        echo "FAIL: no available simulator called \"$device\"." >&2
        echo >&2
        echo "Installed runtimes and devices:" >&2
        xcrun simctl list devices available | sed 's/^/  /' >&2
        echo >&2
        echo "If that list is empty, no iOS runtime is installed: Xcode >" >&2
        echo "Settings > Components. Pass another device with --iphone/--ipad." >&2
        exit 1
    fi

    echo "==> $label: $device"
    xcrun simctl boot "$udid" 2>/dev/null || true
    xcrun simctl bootstatus "$udid" -b >/dev/null 2>&1 || true
    open -a Simulator --args -CurrentDeviceUDID "$udid" || true

    xcrun simctl get_app_container "$udid" "$BUNDLE_ID" >/dev/null 2>&1 || fail \
        "$BUNDLE_ID is not installed on $device. Build it first:
    cd mobile && npx expo run:ios --configuration Release --device \"$device\""

    mkdir -p "$OUT/$label"

    for entry in "${FRAMES[@]}"; do
        local name="${entry%%|*}"
        local description="${entry#*|}"
        [ -z "$ONE_FRAME" ] || [ "$ONE_FRAME" = "$name" ] || continue

        local path="$OUT/$label/$name.png"
        echo
        echo "  $name — $description"
        if [ -t 0 ]; then
            printf '  Set the screen up, then press Enter to capture (s to skip): '
            read -r answer
            [ "$answer" != "s" ] || { echo "  skipped"; continue; }
        else
            fail "no terminal to prompt on; run this interactively"
        fi

        xcrun simctl io "$udid" screenshot --type=png "$path" >/dev/null 2>&1 \
            || fail "the capture failed"

        local width height actual accepted
        width=$(sips -g pixelWidth "$path" | awk '/pixelWidth/ {print $2}')
        height=$(sips -g pixelHeight "$path" | awk '/pixelHeight/ {print $2}')
        actual="${width}x${height}"
        accepted=0
        for size in $(sizes_for "$label"); do
            [ "$actual" = "$size" ] && accepted=1
        done

        if [ "$accepted" = 1 ]; then
            echo "  saved $path ($actual)"
        else
            rm -f "$path"
            fail "$device produced $actual, which App Store Connect will not take for
$label (it accepts: $(sizes_for "$label")). Use a device in the right size class."
        fi
    done
}

[ "$ONLY" = "ipad" ] || capture_device iphone-6.9 "$IPHONE"
[ "$ONLY" = "iphone" ] || capture_device ipad-13 "$IPAD"

echo
echo "Captured into $OUT. Check each one against docs/APPSTORE.md §8 before"
echo "uploading: the set is the story of a transfer, and it is submitted in the"
echo "order it is numbered."
