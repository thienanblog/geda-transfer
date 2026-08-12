# Docker

The receiver as a container, for NAS boxes. amd64 and arm64.

```
docker compose up -d
docker compose exec gedad gedad pair
```

The second command draws a QR code in your terminal. Scan it with the app. If
the window is too narrow for the code, the command says so and prints a URI you
can type in instead.

## Build

```
docker buildx build --platform linux/amd64,linux/arm64 \
    -f docker/Dockerfile -t geda/gedad:latest .
```

The build stage runs on the *build* platform and cross-compiles. `core/` is
CGO-free by rule, which is what makes the arm64 image a compile rather than a
QEMU emulation.

## Volumes

| Path | Holds |
|---|---|
| `/data` | Received files. Point this at your share. |
| `/state` | The ledger and the TLS identity. **Must be persistent.** |

If `/state` is lost, this receiver gets a new identity key, and every paired
phone sees a pin mismatch — a hard failure with no override, fixed only by
pairing every device again (AGENTS.md §3.5).

## Networking

`compose.yml` uses `network_mode: host`, and that is the recommended setup.
Discovery is broadcast, multicast, and a unicast sweep of the local network;
a bridge network hides all three behind NAT. Host networking also means the
addresses the daemon advertises are addresses your phone can actually dial.

Where host networking is unavailable — Docker Desktop on macOS and Windows,
some NAS UIs — use [`compose.bridge.yml`](compose.bridge.yml), which publishes
the ports and sets `GEDA_ADVERTISE` to the host's LAN address by hand. Without
that variable a bridged container hands every phone an address that cannot be
reached: pairing appears to work, and then nothing transfers.

## Configuration

Every setting is an environment variable: `GEDA_` plus the setting name in
capitals. See [`cli/README.md`](../cli/README.md) for the list.

## Permissions

The image runs as uid 1000. Bind mounts arrive with the host's ownership, so
either `chown 1000:1000` the directory or set `user:` to the owner it already
has.
