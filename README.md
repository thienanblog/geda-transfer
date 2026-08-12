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
- **Desktop → Mobile**: send photos, videos, or any file to your phone.
- **Originals by default**: HEIC stays HEIC, ProRAW stays DNG, Live Photos stay
  paired. Format conversion happens on your computer, not on your phone.
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
