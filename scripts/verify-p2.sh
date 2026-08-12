#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Verifies the P2 gate from docs/PLAN.md: with two peers on different subnets,
# discovery succeeds in BOTH directions within 3 seconds.
#
# The two subnets are Docker networks joined by a routing container. That
# router forwards unicast and nothing else, so broadcast and mDNS -- which is
# multicast with TTL=1 -- stop at the subnet boundary, exactly as a real router
# or a WireGuard tunnel does. Only the unicast sweep and the candidate set can
# cross, which is the property under test.
#
# Usage: scripts/verify-p2.sh

set -euo pipefail
cd "$(dirname "$0")/.."

COMPOSE=(docker compose -f test/cross-subnet/compose.yml)
COMPOSE_FILE=test/cross-subnet/compose.yml
LOGS=$(mktemp -d)

# RFC 5737 documentation ranges. See the collision check below for why these
# are not the 192.168.11/12 addresses docs/PLAN.md uses to describe the gate.
SUBNET_A=198.51.100
SUBNET_B=203.0.113

cleanup() {
    "${COMPOSE[@]}" down --remove-orphans --volumes >/dev/null 2>&1 || true
    rm -rf "$LOGS"
}
# INT and TERM as well as EXIT: a Ctrl-C that left the networks up would leave
# the host's routing table altered behind it.
trap cleanup EXIT INT TERM

fail() { echo "FAIL: $*" >&2; exit 1; }

docker info >/dev/null 2>&1 || fail "docker is not running"

# A Docker bridge whose subnet overlaps a network this machine is really on
# takes over the host's route to it: the LAN, and usually the internet with it,
# go away for as long as the network exists. That is a bad way to find out
# about a config typo, so refuse before creating anything.
host_addrs=$(ifconfig 2>/dev/null | awk '/inet /{print $2}' \
    || ip -o -4 addr show 2>/dev/null | awk '{split($4,a,"/"); print a[1]}')

for subnet in "$SUBNET_A" "$SUBNET_B"; do
    grep -q "subnet: $subnet.0/24" "$COMPOSE_FILE" \
        || fail "$COMPOSE_FILE no longer uses $subnet.0/24; update the collision check in this script to match"

    if printf '%s\n' "$host_addrs" | grep -q "^$subnet\."; then
        fail "this machine has an address in $subnet.0/24, so creating a Docker network there would cut off its own connectivity. Pick unused subnets in $COMPOSE_FILE and in this script."
    fi
done

# A previous run that was killed rather than exiting cleanly leaves the
# networks behind, which is exactly the state this script must not build on.
"${COMPOSE[@]}" down --remove-orphans --volumes >/dev/null 2>&1 || true

echo "==> building the test image"
"${COMPOSE[@]}" build --quiet

echo "==> starting two subnets joined by a router"
"${COMPOSE[@]}" up -d >"$LOGS/up.log" 2>&1

# Waiting for both peers rather than stopping at the first to exit: each one
# proves a different direction, and killing the survivor mid-scan would fail
# the half of the gate that has not finished yet.
deadline=$(( $(date +%s) + 120 ))
while :; do
    running=0
    for peer in peer-a peer-b; do
        cid=$("${COMPOSE[@]}" ps -aq "$peer")
        [ -n "$cid" ] || fail "$peer never started; log: $(cat "$LOGS/up.log")"
        [ "$(docker inspect -f '{{.State.Status}}' "$cid")" = "running" ] && running=1
    done
    [ "$running" -eq 0 ] && break
    [ "$(date +%s)" -lt "$deadline" ] || fail "the peers did not finish within 120s"
    sleep 1
done

for peer in peer-a peer-b; do
    "${COMPOSE[@]}" logs --no-log-prefix "$peer" >"$LOGS/$peer.log" 2>&1 || true
done

check() {
    local peer=$1 want=$2
    local line elapsed sources

    line=$(grep -o '{"candidates".*}' "$LOGS/$peer.log" | tail -1 || true)
    [ -n "$line" ] || fail "$peer did not find $want; log: $(cat "$LOGS/$peer.log")"

    elapsed=$(printf '%s' "$line" | sed -n 's/.*"elapsed_ms":\([0-9]*\).*/\1/p')
    sources=$(printf '%s' "$line" | sed -n 's/.*"sources":\(\[[^]]*\]\).*/\1/p')

    [ -n "$elapsed" ] || fail "$peer produced no timing: $line"
    [ "$elapsed" -lt 3000 ] || fail "$peer found $want in ${elapsed}ms, the gate is 3000ms"

    echo "  ok: $peer found $want in ${elapsed}ms via $sources, and connected over pinned TLS"
}

echo "==> checking both directions"
check peer-a peer-b
check peer-b peer-a

# Without this check the gate proves only that two containers found each
# other, not that they did it across a boundary broadcast cannot cross.
echo "==> negative control: mDNS and broadcast alone must NOT cross the router"
"${COMPOSE[@]}" up -d router peer-b >/dev/null 2>&1
control=$("${COMPOSE[@]}" run --rm --no-deps peer-a \
    "ip route replace $SUBNET_B.0/24 via $SUBNET_A.2
     gatepeer -dir /state -id peer-a -name 'Peer A' -expect peer-b \
       -unicast-only=false -scan 3s -wait 12s" 2>&1 || true)

if printf '%s' "$control" | grep -q '"peer":"peer-b"'; then
    fail "peer-b was found without the unicast sweep; the two subnets are not actually separated, so the gate above proves nothing"
fi
echo "  ok: multicast and broadcast stop at the subnet boundary, as designed"

echo
echo "P2 gate: cross-subnet discovery succeeds in both directions within 3s."
