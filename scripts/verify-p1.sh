#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies the P1 gate from docs/PLAN.md with a real server and real curl:
#   1. curl can upload a file
#   2. an interrupted upload resumes from the reported offset
#   3. re-uploading identical content is skipped
#
# Usage: scripts/verify-p1.sh

set -euo pipefail
cd "$(dirname "$0")/.."

STATE=$(mktemp -d)
# A fixed port collides with a receiver the developer already has running, and
# the failure looks like a bug in the server rather than a busy port.
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

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok: $*"; }

echo "==> building receiver"
# Built rather than "go run": go run execs the compiled binary as a child, so
# killing the go process on exit would leave the server holding the port.
(cd core && go build -o "$STATE/devserver" ./internal/devserver)

echo "==> starting receiver on port $PORT"
# tusd logs every request at INFO through its own default logger, which
# would bury this script's output. Keep it, but out of the way.
"$STATE/devserver" -dir "$STATE" -addr "127.0.0.1:$PORT" -token "$TOKEN" >"$STATE/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 100); do
    if "${CURL[@]}" --fail "$BASE/v1/info" >/dev/null 2>&1; then break; fi
    sleep 0.2
done
"${CURL[@]}" --fail "$BASE/v1/info" >/dev/null || fail "server did not come up; log: $(cat "$STATE/server.log")"

# The server speaks HTTP/2 over TLS 1.3, which is what iOS background
# URLSession will negotiate.
HTTP_VERSION=$("${CURL[@]}" -o /dev/null -w '%{http_version}' "$BASE/v1/info")
[ "$HTTP_VERSION" = "2" ] || fail "expected HTTP/2, got '$HTTP_VERSION'"

# curl's --write-out has no portable variable for the TLS version, so read it
# from the handshake trace instead.
"${CURL[@]}" -v -o /dev/null "$BASE/v1/info" 2>&1 | grep -q 'TLSv1.3' \
    || fail "connection did not negotiate TLS 1.3"
pass "HTTP/2 over TLS 1.3"

# A 3 MiB payload, big enough that the interruption below is meaningful.
SRC="$STATE/source.bin"
head -c 3145728 /dev/urandom > "$SRC"
SIZE=$(wc -c < "$SRC" | tr -d ' ')

# The resume test needs its own content. Reusing the first payload would be
# deduplicated on arrival, which is correct behaviour but would mean the resume
# never actually stored anything.
SRC2="$STATE/source2.bin"
head -c 3145728 /dev/urandom > "$SRC2"
SIZE2=$(wc -c < "$SRC2" | tr -d ' ')

echo "==> 1. upload"
META="filename $(b64 'IMG_0001.HEIC'),captured_at $(b64 '2026-07-04T15:09:03Z'),kind $(b64 photo)"
CREATE_HEADERS=$("${CURL[@]}" -D - -o /dev/null -X POST "$BASE/v1/files/" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Length: $SIZE" \
    -H "Upload-Metadata: $META" | tr -d '\r')
LOCATION=$(echo "$CREATE_HEADERS" | awk '/^[Ll]ocation:/ {print $2}')
[ -n "$LOCATION" ] || fail "no Location returned; response was: $CREATE_HEADERS"

STORED=$("${CURL[@]}" -D - -o /dev/null -X PATCH "$LOCATION" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Offset: 0" \
    -H "Content-Type: application/offset+octet-stream" \
    --data-binary "@$SRC" \
    | tr -d '\r' | awk '/^[Gg]eda-[Ss]tored-[Pp]ath:/ {print $2}' | base64 -d)
[ -n "$STORED" ] || fail "upload did not report a stored path"

DEST="$STATE/Photos/$STORED"
[ -f "$DEST" ] || fail "file not found at $DEST"
cmp -s "$SRC" "$DEST" || fail "stored file differs from the source"
pass "uploaded to $STORED, contents match"

echo "==> 2. interrupted upload resumes"
META2="filename $(b64 'VID_0002.MOV'),captured_at $(b64 '2026-07-04T15:09:03Z'),kind $(b64 video)"
LOC2=$("${CURL[@]}" -D - -o /dev/null -X POST "$BASE/v1/files/" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Length: $SIZE2" \
    -H "Upload-Metadata: $META2" \
    | tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')

# Send the first megabyte, then kill the transfer mid-flight.
CUT=1048576
head -c "$CUT" "$SRC2" > "$STATE/part1.bin"
"${CURL[@]}" -o /dev/null -X PATCH "$LOC2" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Offset: 0" \
    -H "Content-Type: application/offset+octet-stream" \
    --data-binary "@$STATE/part1.bin" || true

OFFSET=$("${CURL[@]}" -D - -o /dev/null --head "$LOC2" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Tus-Resumable: 1.0.0" \
    | tr -d '\r' | awk '/^[Uu]pload-[Oo]ffset:/ {print $2}')
[ "$OFFSET" = "$CUT" ] || fail "resume offset is '$OFFSET', expected $CUT"
pass "server reports offset $OFFSET after interruption"

tail -c "+$((CUT + 1))" "$SRC2" > "$STATE/part2.bin"
STORED2=$("${CURL[@]}" -D - -o /dev/null -X PATCH "$LOC2" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Offset: $OFFSET" \
    -H "Content-Type: application/offset+octet-stream" \
    --data-binary "@$STATE/part2.bin" \
    | tr -d '\r' | awk '/^[Gg]eda-[Ss]tored-[Pp]ath:/ {print $2}' | base64 -d)
[ -n "$STORED2" ] || fail "resumed upload did not complete"
cmp -s "$SRC2" "$STATE/Photos/$STORED2" || fail "resumed file differs from the source"
pass "resumed from $OFFSET and the result matches"

echo "==> 3. repeat upload is skipped"
LOC3=$("${CURL[@]}" -D - -o /dev/null -X POST "$BASE/v1/files/" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Length: $SIZE" \
    -H "Upload-Metadata: $META" \
    | tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')
HEADERS=$("${CURL[@]}" -D - -o /dev/null -X PATCH "$LOC3" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Tus-Resumable: 1.0.0" \
    -H "Upload-Offset: 0" \
    -H "Content-Type: application/offset+octet-stream" \
    --data-binary "@$SRC" | tr -d '\r')

echo "$HEADERS" | grep -qi '^geda-deduplicated: 1' || fail "repeat upload was not deduplicated"
REPEAT=$(echo "$HEADERS" | awk '/^[Gg]eda-[Ss]tored-[Pp]ath:/ {print $2}' | base64 -d)
[ "$REPEAT" = "$STORED" ] || fail "dedup pointed at '$REPEAT', expected '$STORED'"
pass "repeat upload skipped, pointing at the existing $STORED"

# Two distinct files uploaded, plus one skipped: three uploads, two files.
COUNT=$(find "$STATE/Photos" -type f -not -path '*/.incoming/*' | wc -l | tr -d ' ')
[ "$COUNT" = "2" ] || fail "expected 2 stored files, found $COUNT"
pass "2 files on disk from 3 uploads"

echo
echo "P1 gate: PASS"
