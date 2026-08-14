# Contributing

Thanks for looking. This is a small project with strong opinions, most of which
are written down — read [AGENTS.md](AGENTS.md) before you write code. It is the
architecture guide, and it records decisions that are already settled and the
reasons for them. A patch that relitigates one of those without arguing with the
reason will be turned down, and that is a waste of your afternoon.

## Sign your commits — the DCO

Contributions are accepted under the
[Developer Certificate of Origin](https://developercertificate.org/) 1.1. There
is no CLA and nothing to sign up for: you certify the DCO by adding a
`Signed-off-by` line to each commit, which git does for you:

```bash
git commit -s -m "your message"
```

That adds a line reading `Signed-off-by: Your Name <your@email>` using your
`user.name` and `user.email`. It must be a real name and a real address you can
receive mail at — the DCO is a statement about provenance, and a pseudonymous
one certifies nothing.

By signing off you are saying you wrote the patch, or that you have the right
to submit it under the Apache-2.0 licence of this project. The full text is in
the link above; it is 200 words and worth reading once.

Forgot to sign off? `git commit --amend -s` for the last commit, or
`git rebase --signoff origin/main` for a branch. CI checks every commit in a
pull request.

## Before you open a pull request

- **`core/` is the source of truth.** `desktop/`, `cli/`, and `docker/` are
  presentation layers over it. If logic is going into one of them, it belongs
  in `core/` instead.
- **Tests are green.** `go test ./...` in `core/`, and whatever else your change
  touches — `cli/`, `desktop/`, and `npm test` in `mobile/`.
- **Every source file carries the Apache-2.0 header.**
  `./scripts/check-headers.sh` tells you which ones do not.
- **No new GPL or AGPL dependency.** They are incompatible with App Store
  distribution, which would end the project. ffmpeg and libheif are invoked as
  external binaries and must never be linked or vendored.
- **A wire-format change updates [docs/PROTOCOL.md](docs/PROTOCOL.md)** in the
  same commit. That document is normative.
- **An architectural decision is appended to
  [docs/DECISIONS.md](docs/DECISIONS.md)**, with the reason. Newest at the
  bottom.
- **A performance claim carries a before and after number.** Speed is the
  headline feature of this app; "should be faster" is not a measurement.

## How the work is organised

Phases, in order, from [docs/PLAN.md](docs/PLAN.md) — one branch each, merged
with `--no-ff` so the phase stays visible in the history. Each phase ends at a
gate that is a script in [scripts/](scripts), and a gate that cannot be verified
is a phase that is not done.

You do not have to work that way to contribute a fix. It is how the features
get built.

## Running things

```bash
cd core && go test ./...                 # the engine, and where most tests live
cd cli && go run . --help                # the headless daemon
cd desktop/frontend && npm ci && npm run build && cd .. && wails build
cd mobile && npm ci && npm test          # plain TypeScript, no device needed
```

The mobile app needs a custom development client — Expo Go cannot load the
native transfer module. See [mobile/AGENTS.md](mobile/AGENTS.md).

The phase gates run against real servers over real TLS, and several of them
need Docker:

```bash
./scripts/verify-p1.sh
./scripts/verify-p2.sh   # two subnets and a router between them
```

## Bug reports

The useful ones say what you did, what happened, and what you expected —
plus both sides: the phone's iOS version and the receiver's platform, whether
they are on the same subnet, and whether a VPN is involved. Discovery bugs are
almost always a network topology, and the topology is the part we cannot guess.

If it involves data loss, say so in the first line.

## Security

Do not open an issue for a vulnerability. [SECURITY.md](SECURITY.md) says where
to send it.

## Code of conduct

[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). It is the Contributor Covenant, and
it applies here as much as anywhere.
