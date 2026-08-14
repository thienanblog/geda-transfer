#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies the P10 gate from docs/PLAN.md:
#
#   "every answer the App Store asks for is written down and matches the app;
#    the repository is one a stranger can use, report to, and contribute to"
#
# P10 is the phase with no code to point at, which is exactly why it needs a
# script. Everything it produces is an assertion about the app made somewhere
# other than the app -- a purpose string in a submission form, a background
# mode in a review note, a permission described in a privacy policy -- and an
# assertion like that is only true on the day it is written. The failure mode is
# not a crash: it is a rejection email a week later, or worse, a shipped
# description of a thing the app no longer does.
#
# So this checks the submission against the resolved configuration rather than
# against itself:
#
#   * what iOS will be told. `expo config --type introspect` produces the
#     Info.plist and privacy manifest prebuild would write. Every purpose string
#     must exist, be about something the app actually does, and not be one of
#     Expo's templates; the background modes must be exactly the one that is
#     justified; the privacy manifest must declare what first-party code uses
#     and nothing it does not.
#
#   * what the dossier says iOS will be told. docs/APPSTORE.md quotes the
#     purpose strings, and a dossier that has drifted from the app is worse than
#     none, because somebody will paste it into App Store Connect.
#
#   * what the app says when the Local Network permission is refused, which is
#     the one permission with no API to ask about and no second prompt.
#
#   * what the repository offers somebody who arrives from the App Store
#     listing: a licence, a way to report a vulnerability privately, a way to
#     contribute under the DCO, and no dead links in any of it.
#
# What is left to a person is the screenshots: they are pictures of a built app
# on a real screen, and this script reports which are missing rather than
# inventing any. `--strict` turns that into an error, which is what to use when
# claiming the phase is done.
#
# Usage: scripts/verify-p10.sh [--strict]

set -euo pipefail
cd "$(dirname "$0")/.."

STRICT=0
[ "${1:-}" = "--strict" ] && STRICT=1

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok: $*"; }

command -v node >/dev/null || fail "node is not installed"
command -v python3 >/dev/null || fail "python3 is not installed"

WORK=$(mktemp -d)
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# What iOS will be told
# ---------------------------------------------------------------------------

echo "==> the configuration the App Store will see"

[ -d mobile/node_modules ] || (cd mobile && npm install --no-audit --no-fund >/dev/null) \
    || fail "npm install failed"

# Resolved from a project root with no `ios/` in it, which is the state EAS
# builds from. `expo config --type introspect` merges the *existing* native
# project when there is one, so a stale local prebuild would answer for keys
# app.json no longer sets -- and the gate would pass on a machine that had
# once run prebuild while the shipped binary said something else. The symlink
# farm is everything mobile/ has except the prebuild output.
mkdir -p "$WORK/mobile"
for entry in mobile/*; do
    name=$(basename "$entry")
    [ "$name" = "ios" ] && continue
    ln -s "$PWD/$entry" "$WORK/mobile/$name"
done

(cd "$WORK/mobile" && npx expo config --type introspect --json 2>/dev/null) > "$WORK/config.json" \
    || fail "could not resolve the Expo configuration"
[ -s "$WORK/config.json" ] || fail "the resolved configuration is empty"
pass "the configuration resolves from a clean root, plugins and all"

# The identifier for the background kick-off, which has to be the same string
# in three places: the plist that permits it, the Swift that registers it, and
# the TypeScript that asks for it. A mismatch is silent -- iOS simply never
# runs the task, and automatic backup quietly stops working.
KICKOFF=$(grep -hoE 'kickoffIdentifier = "[^"]+"' mobile/modules/geda-transfer/ios/*.swift \
    | head -1 | sed -E 's/.*"(.*)"/\1/' || true)
[ -n "$KICKOFF" ] || fail "no kickoffIdentifier in the native sources; the plist has nothing to agree with"

python3 - "$WORK/config.json" "$KICKOFF" <<'PYTHON' || exit 1
import json, sys

config = json.load(open(sys.argv[1]))
kickoff = sys.argv[2]
ios = config.get("ios", {})
plist = ios.get("infoPlist", {})
problems = []
def ok(message): print(f"  ok: {message}")

# --- purpose strings ------------------------------------------------------
#
# Every permission the app asks for, and no permission it does not. A string
# for a capability the app never exercises is a 5.1.1 rejection, and Expo's
# plugins add two of them by default.
expected = {
    "NSPhotoLibraryUsageDescription",
    "NSPhotoLibraryAddUsageDescription",
    "NSCameraUsageDescription",
    "NSLocalNetworkUsageDescription",
}
present = {key for key in plist if key.endswith("UsageDescription")}

wrong_set = []
for key in sorted(expected - present):
    wrong_set.append(f"{key} is missing; the app asks for that permission")
for key in sorted(present - expected):
    wrong_set.append(f"{key} describes a permission this app does not use")
problems += wrong_set
if not wrong_set:
    ok(f"{len(expected)} purpose strings, one per permission the app actually asks for")

# Only the strings that are actually there can be judged on their wording.
wording = []
for key in sorted(present & expected):
    value = plist[key]
    # "Allow $(PRODUCT_NAME) to access your microphone" and its relatives.
    if "$(PRODUCT_NAME)" in value or value.startswith("Allow "):
        wording.append(f"{key} is still Expo's template text")
    elif len(value) < 40:
        wording.append(f"{key} is too short to explain anything: {value!r}")
problems += wording
if not wording and present & expected:
    ok(f"none of the {len(present & expected)} is boilerplate, and each says what the data is for")

if "NSBonjourServices" in plist:
    if "_gedatransfer._tcp" not in plist["NSBonjourServices"]:
        problems.append("NSBonjourServices does not list _gedatransfer._tcp")
    else:
        ok("Bonjour is declared, without which mDNS discovery cannot work at all")
else:
    problems.append("NSBonjourServices is missing; mDNS discovery would fail silently")

# --- background modes -----------------------------------------------------
#
# Exactly one, and it is the one docs/APPSTORE.md justifies. `fetch` is the
# cargo-culted addition that would be asking for a capability never used.
modes = plist.get("UIBackgroundModes", [])
if modes != ["processing"]:
    problems.append(f"UIBackgroundModes is {modes}, not exactly ['processing']")
else:
    ok("one background mode, and it is the one with a justification written down")

ids = plist.get("BGTaskSchedulerPermittedIdentifiers", [])
if ids != [kickoff]:
    problems.append(f"BGTaskSchedulerPermittedIdentifiers is {ids}, but the native code registers {kickoff!r}")
else:
    ok(f"the permitted task identifier is the one the app registers ({kickoff})")

if plist.get("ITSAppUsesNonExemptEncryption") is not False:
    problems.append("ITSAppUsesNonExemptEncryption is not declared false")
else:
    ok("export compliance is answered in the binary, so no upload stalls on it")

if plist.get("NSSupportsLiveActivities") is not True:
    problems.append("NSSupportsLiveActivities is not true; the Live Activity would never appear")
else:
    ok("Live Activities are declared")

# --- privacy manifest -----------------------------------------------------
manifest = ios.get("privacyManifests")
manifest_problems_before = len(problems)
if not manifest:
    problems.append("there is no privacy manifest; the upload is answered by ITMS-91053")
else:
    if manifest.get("NSPrivacyTracking") is not False:
        problems.append("NSPrivacyTracking is not false")
    if manifest.get("NSPrivacyTrackingDomains"):
        problems.append("NSPrivacyTrackingDomains is not empty on an app that does not track")
    if manifest.get("NSPrivacyCollectedDataTypes"):
        problems.append("NSPrivacyCollectedDataTypes is not empty, but App Privacy answers 'not collected'")

    declared = {
        entry.get("NSPrivacyAccessedAPIType"): entry.get("NSPrivacyAccessedAPITypeReasons", [])
        for entry in manifest.get("NSPrivacyAccessedAPITypes", [])
    }
    if "NSPrivacyAccessedAPICategoryFileTimestamp" not in declared:
        problems.append("the manifest does not declare FileTimestamp, which the app uses on its staged copies")
    elif "C617.1" not in declared["NSPrivacyAccessedAPICategoryFileTimestamp"]:
        problems.append("FileTimestamp is declared without C617.1, the reason for files in the app container")
    if len(problems) == manifest_problems_before:
        ok("the privacy manifest declares no tracking, no collection, and the one API category used")

for problem in problems:
    print(f"FAIL: {problem}", file=sys.stderr)
sys.exit(1 if problems else 0)
PYTHON

# A category the manifest does not declare must not appear in first-party code.
# Dependencies declare their own; this catches the day somebody reaches for
# UserDefaults or free-disk-space here and the manifest stops being true.
UNDECLARED=$(grep -rlE 'UserDefaults|volumeAvailableCapacity|systemFreeSize|systemUptime|mach_absolute_time' \
    mobile/modules/geda-transfer/ios mobile/ios-extensions mobile/src 2>/dev/null || true)
[ -z "$UNDECLARED" ] || fail "first-party code uses an undeclared required-reason API: $UNDECLARED"
pass "no required-reason API is used that the manifest does not declare"

# ---------------------------------------------------------------------------
# What the app says when iOS blocks it
# ---------------------------------------------------------------------------
#
# The Local Network permission is asked once, cannot be queried, and cannot be
# asked for again. An app that does not recognise its own symptom is an app
# that finds nothing forever and says only that nothing answered.

echo
echo "==> the copy for a refused Local Network permission"

(cd mobile && npx vitest run src/core/__tests__/reachability.test.ts >/dev/null 2>&1) \
    || fail "the reachability diagnosis failed its tests"
pass "the diagnosis names the permission only when the symptom actually fits"

for screen in HomeScreen PairScreen TransferScreen; do
    grep -q "SettingsHint" "mobile/src/ui/$screen.tsx" \
        || fail "$screen does not offer a way to Settings when the network is blocked"
done
pass "all three screens that connect offer the way out to Settings"

grep -q "Linking.openSettings" mobile/src/ui/components.tsx \
    || fail "the hint does not actually open Settings"
pass "the hint opens this app's page in Settings"

# ---------------------------------------------------------------------------
# What the dossier claims
# ---------------------------------------------------------------------------

echo
echo "==> the submission dossier agrees with the app"

python3 - "$WORK/config.json" <<'PYTHON' || exit 1
import json, re, sys

plist = json.load(open(sys.argv[1]))["ios"]["infoPlist"]
text = open("docs/APPSTORE.md").read()
problems = []

quoted = dict(re.findall(r'^\|\s*`(NS\w+UsageDescription)`\s*\|\s*"([^"]*)"', text, re.M))
if not quoted:
    problems.append("docs/APPSTORE.md quotes no purpose strings; §7 has lost its table")
for key, value in sorted(quoted.items()):
    if key not in plist:
        problems.append(f"docs/APPSTORE.md documents {key}, which the app no longer asks for")
    elif plist[key] != value:
        problems.append(f"the {key} in docs/APPSTORE.md is not the one the app ships")
for key in sorted(k for k in plist if k.endswith("UsageDescription")):
    if key not in quoted:
        problems.append(f"{key} ships but is undocumented in docs/APPSTORE.md §7")
if not problems:
    print(f"  ok: all {len(quoted)} purpose strings in the dossier are the ones the app ships")

# App Store Connect enforces these two lengths at upload time, silently
# truncating nothing -- it simply refuses the field.
subtitle = re.search(r'^\|\s*Subtitle\s*\|\s*(.+?)\s*\|', text, re.M)
if not subtitle:
    problems.append("the listing table has no subtitle")
elif len(subtitle.group(1)) > 30:
    problems.append(f"the subtitle is {len(subtitle.group(1))} characters; App Store Connect allows 30")
else:
    print(f"  ok: the subtitle fits in 30 characters ({len(subtitle.group(1))})")

keywords = re.search(r'\*\*Keywords\*\*.*?```\n(.+?)\n```', text, re.S)
if not keywords:
    problems.append("the dossier has no keyword line")
else:
    field = keywords.group(1).strip()
    if len(field) > 100:
        problems.append(f"the keyword field is {len(field)} characters; App Store Connect allows 100")
    elif " " in field.replace(" ", "", 0) and ", " in field:
        problems.append("the keyword field has spaces after commas, which waste characters")
    else:
        print(f"  ok: the keyword field fits in 100 characters ({len(field)})")

for problem in problems:
    print(f"FAIL: {problem}", file=sys.stderr)
sys.exit(1 if problems else 0)
PYTHON

grep -q "BGProcessingTaskRequest" docs/APPSTORE.md \
    || fail "the dossier does not justify the processing background mode"
grep -q "no.*entry in .UIBackgroundModes" docs/APPSTORE.md \
    || fail "the dossier does not say that background URLSession needs no background mode"
pass "the background mode is justified, and so is the one that is absent"

# ---------------------------------------------------------------------------
# A repository somebody can arrive at
# ---------------------------------------------------------------------------

echo
echo "==> what a stranger finds in the repository"

for file in LICENSE NOTICE README.md CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md \
            docs/PRIVACY.md docs/APPSTORE.md \
            .github/PULL_REQUEST_TEMPLATE.md .github/ISSUE_TEMPLATE/bug_report.yml \
            .github/ISSUE_TEMPLATE/feature_request.yml .github/ISSUE_TEMPLATE/config.yml; do
    [ -f "$file" ] || fail "$file is missing"
done
pass "licence, notice, readme, contributing, security policy, conduct, templates"

grep -q "Developer Certificate of Origin" CONTRIBUTING.md || fail "CONTRIBUTING.md does not name the DCO"
grep -q "git commit -s" CONTRIBUTING.md || fail "CONTRIBUTING.md does not say how to sign off"
pass "the DCO is explained, with the command that satisfies it"

grep -q "security/advisories/new" SECURITY.md || fail "SECURITY.md gives no private reporting route"
pass "a vulnerability has somewhere private to go"

# Dead links in the documents a newcomer reads first are the cheapest possible
# bad impression, and the easiest to leave behind when a file moves.
python3 - <<'PYTHON' || exit 1
import os, re, subprocess, sys

files = subprocess.run(["git", "ls-files", "*.md"], capture_output=True, text=True,
                       check=True).stdout.split()
broken = []
checked = 0
for path in files:
    body = open(path, encoding="utf-8").read()
    # Skip fenced code: it is full of examples that are not links.
    body = re.sub(r"```.*?```", "", body, flags=re.S)
    for target in re.findall(r"\]\(([^)\s]+)\)", body):
        if re.match(r"^(https?:|mailto:|#)", target):
            continue
        resolved = os.path.normpath(os.path.join(os.path.dirname(path), target.split("#")[0]))
        if not resolved:
            continue
        checked += 1
        if not os.path.exists(resolved):
            broken.append(f"{path} -> {target}")

for link in broken:
    print(f"FAIL: broken link: {link}", file=sys.stderr)
if broken:
    sys.exit(1)
print(f"  ok: {checked} relative links across {len(files)} documents all resolve")
PYTHON

# ---------------------------------------------------------------------------
# Licences
# ---------------------------------------------------------------------------
#
# A GPL or AGPL dependency would relicense the project and end App Store
# distribution with it (AGENTS.md §4). ffmpeg and libheif stay external
# processes for this reason, and nothing may arrive through a package manager
# that undoes it.

echo
echo "==> nothing arrived under a licence that would end the project"

python3 - <<'PYTHON' || exit 1
import json, os, re, sys

def gpl_family(term):
    # "LGPL-3.0" is not "GPL-3.0"; the lookbehind is what keeps them apart.
    return bool(re.search(r"(?<![A-Z])A?GPL", term.upper()))

def offending(licence):
    """Whether a licence expression leaves us no choice but the GPL.

    Dual licences are common and fine: "(BSD-3-Clause OR GPL-2.0)" is a
    package we may take under BSD. Only an expression with no non-GPL branch
    would relicense this project.
    """
    if not licence:
        return False
    alternatives = re.split(r"\bOR\b", licence.replace("(", " ").replace(")", " "),
                            flags=re.I)
    return all(gpl_family(alternative) for alternative in alternatives)

bad, scanned = [], 0
for root in ("mobile/node_modules", "desktop/frontend/node_modules"):
    if not os.path.isdir(root):
        print(f"  --: {root} is not installed; not scanned")
        continue
    for entry in os.scandir(root):
        packages = [entry] if not entry.name.startswith("@") else list(os.scandir(entry.path))
        for package in packages:
            manifest = os.path.join(package.path, "package.json")
            if not os.path.isfile(manifest):
                continue
            try:
                data = json.load(open(manifest, encoding="utf-8"))
            except (ValueError, OSError):
                continue
            licence = data.get("license")
            if isinstance(licence, dict):
                licence = licence.get("type")
            if not isinstance(licence, str):
                licence = " ".join(
                    item.get("type", "") for item in data.get("licenses", [])
                    if isinstance(item, dict))
            scanned += 1
            if offending(licence):
                bad.append(f"{data.get('name', package.name)} ({licence})")

for package in sorted(set(bad)):
    print(f"FAIL: GPL-family dependency: {package}", file=sys.stderr)
if bad:
    sys.exit(1)
if scanned:
    print(f"  ok: {scanned} npm packages, none of them GPL or AGPL")
PYTHON

if command -v go >/dev/null; then
    for module in core cli desktop; do
        (cd "$module" && go list -m -f '{{.Path}} {{.Dir}}' all 2>/dev/null) || true
    done | sort -u > "$WORK/gomodules.txt"

    bad=""
    checked=0
    while read -r _ dir; do
        [ -n "${dir:-}" ] && [ -d "$dir" ] || continue
        for licence in "$dir"/LICENSE* "$dir"/COPYING*; do
            [ -f "$licence" ] || continue
            checked=$((checked + 1))
            if grep -qE 'GNU (AFFERO )?GENERAL PUBLIC LICENSE' "$licence"; then
                bad="$bad $dir"
            fi
            break
        done
    done < "$WORK/gomodules.txt"

    [ -z "$bad" ] || fail "GPL-family Go dependency:$bad"
    if [ "$checked" -gt 0 ]; then
        pass "$checked Go modules carry a licence file, none of them GPL or AGPL"
    else
        echo "  --: no Go module licences in the cache; not scanned"
    fi
fi

# ---------------------------------------------------------------------------
# Screenshots
# ---------------------------------------------------------------------------
#
# Pictures of a built app on a real screen. A machine with no iOS runtime
# cannot make one, and a placeholder in a store listing is a rejection under
# 2.3.3 -- so this reports rather than invents. scripts/screenshots.sh is what
# produces them.

echo
echo "==> the screenshot set"

missing=0
for frame in 01-home 02-pairing 03-transfer 04-live-activity 05-inbox 06-send-options; do
    for device in iphone-6.9 ipad-13; do
        file="docs/appstore/screenshots/$device/$frame.png"
        if [ -f "$file" ]; then
            pass "$file"
        else
            echo "  --: $file has not been captured"
            missing=$((missing + 1))
        fi
    done
done

echo
if [ "$missing" -eq 0 ]; then
    echo "P10 gate: the submission answers match the app, the repository is one a"
    echo "stranger can use, and the screenshot set is complete."
    exit 0
fi

if [ "$STRICT" = 1 ]; then
    cat >&2 <<MESSAGE
FAIL: $missing screenshots have not been captured.

Run scripts/screenshots.sh on a Mac with an iOS runtime installed and the app
built. A store listing cannot be submitted without them, and nothing here will
invent one.

An uncaptured screenshot set is not a passed gate.
MESSAGE
    exit 1
fi

cat <<MESSAGE
Everything a machine can check is checked and green. $missing screenshots are
missing: they are pictures of a built app on a real screen, they are not
claimed here, and scripts/screenshots.sh takes them.
MESSAGE
