#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies the P3 gate from docs/PLAN.md: a container receives an upload from
# curl on another subnet.
#
# The receiver is the real image from docker/Dockerfile, run exactly as it
# ships -- unprivileged user, default entrypoint, no test code inside it. The
# client is curl and openssl on a different Docker network, so every packet
# between them is routed rather than switched.
#
# What is checked, in order:
#   1. the two containers really are on different subnets
#   2. the image is paired with through its control socket, as a NAS admin does
#   3. the certificate the receiver serves matches the SPKI pin in the QR code
#   4. curl uploads a file across the subnet boundary, byte for byte
#   5. re-uploading the same file is deduplicated
#   6. the daemon reports the device and the file it holds
#   7. the image builds for arm64 as well as amd64 -- most NAS boxes are arm
#
# Usage: scripts/verify-p3.sh

set -euo pipefail
cd "$(dirname "$0")/.."

COMPOSE=(docker compose -f test/nas/compose.yml)
COMPOSE_FILE=test/nas/compose.yml
WORK=$(mktemp -d)

CLIENT_SUBNET=198.51.100
NAS_SUBNET=203.0.113
NAS_ADDR=$NAS_SUBNET.30
BASE="https://$NAS_ADDR:47891"

# Multi-arch builds need a builder that is not the default docker driver,
# which can only produce one platform at a time.
BUILDER=geda-p3-multiarch
BUILDER_CREATED=

cleanup() {
    "${COMPOSE[@]}" down --remove-orphans --volumes >/dev/null 2>&1 || true
    [ -n "$BUILDER_CREATED" ] && docker buildx rm "$BUILDER" >/dev/null 2>&1
    rm -rf "$WORK"
    return 0
}
trap cleanup EXIT INT TERM

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok: $*"; }

# Runs a command inside the client container.
client() { "${COMPOSE[@]}" exec -T client sh -c "$1"; }
# Runs a command inside the receiver, as the NAS admin would over SSH.
nas() { "${COMPOSE[@]}" exec -T gedad sh -c "$1"; }

docker info >/dev/null 2>&1 || fail "docker is not running"

# A Docker bridge overlapping a network this machine is really on takes over
# the host's route to it. Refuse before creating anything -- see
# docs/DECISIONS.md.
host_addrs=$(ifconfig 2>/dev/null | awk '/inet /{print $2}' \
    || ip -o -4 addr show 2>/dev/null | awk '{split($4,a,"/"); print a[1]}')

for subnet in "$CLIENT_SUBNET" "$NAS_SUBNET"; do
    grep -q "subnet: $subnet.0/24" "$COMPOSE_FILE" \
        || fail "$COMPOSE_FILE no longer uses $subnet.0/24; update this script to match"

    if printf '%s\n' "$host_addrs" | grep -q "^$subnet\."; then
        fail "this machine has an address in $subnet.0/24, so creating a Docker network there would cut off its own connectivity. Pick unused subnets in $COMPOSE_FILE and in this script."
    fi
done

"${COMPOSE[@]}" down --remove-orphans --volumes >/dev/null 2>&1 || true

echo "==> building the receiver image and the client"
"${COMPOSE[@]}" build --quiet

echo "==> starting the receiver on $NAS_SUBNET.0/24 and the client on $CLIENT_SUBNET.0/24"
"${COMPOSE[@]}" up -d >"$WORK/up.log" 2>&1 || fail "compose up failed: $(cat "$WORK/up.log")"

deadline=$(( $(date +%s) + 90 ))
until nas 'gedad status' >"$WORK/status.txt" 2>&1; do
    [ "$(date +%s)" -lt "$deadline" ] \
        || fail "the daemon never answered its control socket; logs: $("${COMPOSE[@]}" logs --no-log-prefix gedad 2>&1 | tail -20)"
    sleep 1
done
pass "receiver up: $(awk '/^name/{ $1=""; print substr($0,2) }' "$WORK/status.txt")"

echo "==> 1. the client is on another subnet"
# "via" in the route means a gateway hop. Without this check the gate would
# still pass with both containers on one network, and prove nothing.
route=$(client "ip route get $NAS_ADDR")
printf '%s' "$route" | grep -q ' via ' \
    || fail "the client reaches $NAS_ADDR directly, so this is not a cross-subnet test: $route"
pass "$NAS_ADDR is reached through a gateway: $(printf '%s' "$route" | head -1)"

echo "==> 2. pairing through the control socket"
# This is what a NAS admin does over SSH. The QR code is the same payload.
offer=$(nas 'gedad pair -json -ttl 5m')
uri=$(printf '%s' "$offer" | sed -n 's/.*"uri": *"\([^"]*\)".*/\1/p')
pin=$(printf '%s' "$offer" | sed -n 's/.*"spki": *"\([^"]*\)".*/\1/p')
[ -n "$uri" ] && [ -n "$pin" ] || fail "gedad pair returned no usable offer: $offer"

# Decode the QR payload the way the mobile app will: base64url of compact JSON.
b64=${uri#geda://pair/}
case $(( ${#b64} % 4 )) in
    2) b64="$b64==" ;;
    3) b64="$b64=" ;;
esac
payload=$(printf '%s' "$b64" | tr '_-' '/+' | base64 -d)
psk=$(printf '%s' "$payload" | sed -n 's/.*"psk":"\([^"]*\)".*/\1/p')
receiver_id=$(printf '%s' "$payload" | sed -n 's/.*"device_id":"\([^"]*\)".*/\1/p')
[ -n "$psk" ] || fail "no pairing secret in the payload: $payload"
pass "offer issued for receiver $receiver_id"

echo "==> 3. the served certificate matches the pinned key"
# The receiver is up on its own subnet, but reaching it from the other one
# goes through the router; wait for that path before concluding anything about
# certificates.
deadline=$(( $(date +%s) + 60 ))
until client "curl -sS -k --max-time 5 -o /dev/null $BASE/v1/info" >/dev/null 2>&1; do
    [ "$(date +%s)" -lt "$deadline" ] \
        || fail "the client could not reach $BASE across the router: $(client "curl -sS -k --max-time 5 $BASE/v1/info" 2>&1 | tail -3)"
    sleep 1
done

# Exactly what the phone checks on every later connection: SHA-256 of the
# certificate's SubjectPublicKeyInfo, base64. Not the certificate itself --
# that changes on renewal (AGENTS.md 3.5).
#
# The certificate is fetched on its own rather than inside the digest
# pipeline. An s_client that fails prints nothing, and a pipeline fed nothing
# happily produces the SHA-256 of the empty string -- which looks exactly like
# a pin and turns "could not connect" into "wrong key".
certificate=$(client "openssl s_client -connect $NAS_ADDR:47891 </dev/null 2>/tmp/s_client.err | openssl x509 -pubkey -noout")
if [ -z "$certificate" ]; then
    fail "could not read the receiver's certificate: $(client "cat /tmp/s_client.err" 2>/dev/null | tail -5)"
fi

served=$(printf '%s\n' "$certificate" \
    | client "openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64")
[ -n "$served" ] || fail "the certificate could not be reduced to a pin"
[ "$served" = "$pin" ] || fail "served key pins to '$served', the offer said '$pin'"
pass "SPKI pin matches: $pin"

echo "==> 4. curl uploads across the subnet boundary"
token=$(client "curl -sS -k -X POST $BASE/v1/pair \
    -H 'Content-Type: application/json' \
    -d '{\"v\":1,\"psk\":\"$psk\",\"device_id\":\"gate-phone\",\"name\":\"Gate Phone\",\"platform\":\"linux\"}'" \
    | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || fail "pairing over the network produced no token"

# 3 MiB of random data, hashed on the client before it leaves.
client "head -c 3145728 /dev/urandom > /tmp/source.bin"
size=$(client 'wc -c < /tmp/source.bin' | tr -d ' \r')
source_hash=$(client 'sha256sum /tmp/source.bin' | awk '{print $1}')

# Metadata values are base64 per the tus specification.
meta="filename $(printf '%s' 'IMG_0042.HEIC' | base64 | tr -d '\n'),captured_at $(printf '%s' '2026-07-04T15:09:03Z' | base64 | tr -d '\n'),kind $(printf '%s' photo | base64 | tr -d '\n')"

location=$(client "curl -sS -k -D - -o /dev/null -X POST $BASE/v1/files/ \
    -H 'Authorization: Bearer $token' \
    -H 'Tus-Resumable: 1.0.0' \
    -H 'Upload-Length: $size' \
    -H 'Upload-Metadata: $meta'" | tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')
[ -n "$location" ] || fail "the receiver did not create an upload"

headers=$(client "curl -sS -k -D - -o /dev/null -X PATCH $location \
    -H 'Authorization: Bearer $token' \
    -H 'Tus-Resumable: 1.0.0' \
    -H 'Upload-Offset: 0' \
    -H 'Content-Type: application/offset+octet-stream' \
    --data-binary @/tmp/source.bin" | tr -d '\r')

stored=$(printf '%s' "$headers" | awk '/^[Gg]eda-[Ss]tored-[Pp]ath:/ {print $2}' | base64 -d)
[ -n "$stored" ] || fail "the upload reported no stored path; response: $headers"

# The bytes on the NAS volume, not the receiver's own opinion of them.
stored_hash=$(nas "sha256sum '/data/$stored'" | awk '{print $1}')
[ "$stored_hash" = "$source_hash" ] || fail "stored file differs: $stored_hash vs $source_hash"
pass "$size bytes landed at /data/$stored, hash matches"

echo "==> 5. the same file again is deduplicated"
location2=$(client "curl -sS -k -D - -o /dev/null -X POST $BASE/v1/files/ \
    -H 'Authorization: Bearer $token' \
    -H 'Tus-Resumable: 1.0.0' \
    -H 'Upload-Length: $size' \
    -H 'Upload-Metadata: $meta'" | tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')
repeat=$(client "curl -sS -k -D - -o /dev/null -X PATCH $location2 \
    -H 'Authorization: Bearer $token' \
    -H 'Tus-Resumable: 1.0.0' \
    -H 'Upload-Offset: 0' \
    -H 'Content-Type: application/offset+octet-stream' \
    --data-binary @/tmp/source.bin" | tr -d '\r')

printf '%s' "$repeat" | grep -qi '^geda-deduplicated: 1' || fail "the repeat upload was not deduplicated"
count=$(nas "find /data -type f -not -path '*/.incoming/*' | wc -l" | tr -d ' \r')
[ "$count" = "1" ] || fail "expected 1 file on the volume, found $count"
pass "second upload skipped, one file on the volume"

echo "==> 6. the daemon reports what it holds"
devices=$(nas 'gedad devices')
printf '%s' "$devices" | grep -q 'gate-phone' || fail "the paired device is not listed: $devices"
nas 'gedad status' | grep -q 'files received  *1' || fail "status does not report the received file"
pass "gate-phone is listed with its file"

echo "==> 7. the image builds for arm64 as well as amd64"
# Most NAS boxes are arm64, and cross-compiling from a CGO-free core is the
# only reason that build is quick enough to be part of a gate.
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
    docker buildx create --name "$BUILDER" --driver docker-container >/dev/null \
        || fail "could not create a buildx builder; multi-arch images need the docker-container driver"
    BUILDER_CREATED=1
fi

docker buildx build --builder "$BUILDER" --platform linux/amd64,linux/arm64 \
    -f docker/Dockerfile --output=type=cacheonly . >"$WORK/buildx.log" 2>&1 \
    || fail "multi-arch build failed: $(tail -20 "$WORK/buildx.log")"
pass "linux/amd64 and linux/arm64 both build"

echo
echo "P3 gate: a container receives an upload from curl on another subnet."
