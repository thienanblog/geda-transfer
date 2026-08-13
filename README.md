# Geda Transfer

Fast Wi-Fi transfer of photos, videos, and files between your phone and your
computer or NAS. Local network only — no cloud, no account, no subscription.

**Status:** in development.

## Why

USB transfers are slow and awkward. Cloud sync means uploading your library to
someone else's computer. Modern Wi-Fi is faster than either. Geda Transfer
moves your files directly between devices on your own network.

## What it does

- **Mobile → Desktop/NAS**: manual transfer, or opt-in automatic backup that
  keeps running when your phone is locked.
- **Desktop → Mobile**: send photos, videos, or any file to your phone. Your
  computer cannot wake a sleeping iPhone, so it queues the files and the phone
  collects them when you open the app — continuing in the background after you
  put it down. Photos and videos land in your library, everything else in Files.
- **Originals by default**: HEIC stays HEIC, ProRAW stays DNG, Live Photos stay
  paired. If you want a JPEG or an H.264 copy, your computer makes one after
  the files arrive — on a real CPU, without draining your phone's battery, and
  without touching the original unless you explicitly ask it to.
- **Works across subnets and over VPN**: discovery is not limited to your local
  broadcast domain (see below).
- **Runs headless**: a CLI daemon and Docker image for NAS.

## Cross-subnet and VPN discovery

Most apps in this category use mDNS/Bonjour, which is multicast with TTL=1 —
routers drop it by design, and VPN tunnels have no broadcast domain at all. So
they only ever find devices on the same subnet.

Geda Transfer additionally does unicast discovery: you can add extra subnets to
scan, and a paired computer advertises **all** of its interface addresses,
including VPN addresses. A phone on a remote network reaches your desktop over
WireGuard without any manual configuration.

## Platforms

| | Status |
|---|---|
| iOS / iPadOS | v1 |
| macOS, Windows | v1 |
| Linux / NAS (CLI + Docker) | v1 |
| Android | after v1 |

## Running on a NAS

```
docker compose -f docker/compose.yml up -d
docker compose -f docker/compose.yml exec gedad gedad pair
```

The second command draws a pairing QR code in your terminal. There is also a
plain `gedad` binary with a systemd unit — see [cli/README.md](cli/README.md).

## The desktop app

The macOS and Windows app is a [Wails](https://wails.io) shell over the same
receiver the NAS daemon runs. It shows a pairing code for the phone's camera,
a live view of what is arriving, the history of what has been received, and
settings for where files go and what they are named.

```
cd desktop/frontend && npm ci && npm run build
cd .. && wails build
```

"Send files" on a device queues; it does not transfer. That is not a
limitation of this app — nothing can push to a suspended iPhone — so the
window says "waiting for the phone" until the phone has actually collected the
file and said so.

It keeps receiving when its window is closed — a phone cannot wake a sleeping
computer, so the app has to be running for anything to arrive — and stays
visible in the menu bar or notification area while it does. See
[desktop/README.md](desktop/README.md).

## The mobile app

The iOS app is an Expo project in [mobile/](mobile), built with a custom
development client — Expo Go cannot load the native transfer module. See
[mobile/AGENTS.md](mobile/AGENTS.md).

Measured speeds live in [docs/PERFORMANCE.md](docs/PERFORMANCE.md).

## Building

See [AGENTS.md](AGENTS.md) for architecture and [docs/PLAN.md](docs/PLAN.md)
for the roadmap.

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

The name "Geda Transfer" and the logo are trademarks and are not covered by the
code license; see [TRADEMARK.md](TRADEMARK.md). Forks are welcome under a
different name.

Contributions are accepted under the Developer Certificate of Origin — sign
your commits with `git commit -s`.
