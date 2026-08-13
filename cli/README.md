# gedad

The headless receiver: a NAS, a Linux box, a container. A thin layer over
`core/` (AGENTS.md §2) plus the parts a machine with no screen needs — a config
file, a control socket, and a QR code drawn in the terminal.

```
gedad run        # serve until stopped (the default)
gedad pair       # show a pairing QR code for a phone to scan
gedad devices    # list paired devices
gedad send       # queue files for a phone to collect
gedad queue      # what is waiting for a phone
gedad unpair ID  # revoke a device's token; its files stay
gedad status     # what the running daemon is doing
```

## Sending files to a phone

```
gedad send -device phone-1 ~/Documents/contract.pdf ~/Videos/holiday.mov
gedad queue -device phone-1
gedad queue -device phone-1 -cancel <item-id>
```

`send` queues; it does not transfer. Nothing can push to a suspended iPhone
(AGENTS.md §3.7), so the files are hashed and put on offer, and the phone
collects them the next time somebody opens the app — continuing in the
background if they put it down. `gedad queue` is where you see whether that has
happened yet.

The bytes are not copied anywhere. A queued row points at the file where it
already lives, and the size and mtime seen at queueing time are re-checked
before it is served: editing or deleting the file afterwards fails that item
rather than sending something its digest no longer describes. Files on a mount
that comes and goes are worth copying somewhere stable first.

## Install

```
go build -o gedad ./cli
sudo install -m0755 gedad /usr/local/bin/gedad
sudo install -D -m0644 cli/packaging/gedad.conf /etc/geda/gedad.conf
sudo install -D -m0644 cli/packaging/gedad.service /etc/systemd/system/gedad.service
sudo useradd --system --home /var/lib/geda --shell /usr/sbin/nologin geda
sudo systemctl enable --now gedad
```

Then, to pair a phone:

```
sudo -u geda gedad pair
```

For Docker, see [`docker/`](../docker).

## Configuration

Three sources, later ones winning: the config file, then `GEDA_*` environment
variables, then flags. Every setting is reachable from all three — the file
keys are listed in
[`packaging/gedad.conf`](packaging/gedad.conf), the environment variable for a
key is `GEDA_` plus its name in capitals, and `-set key=value` sets any of them
on the command line.

An unknown key is an error rather than a warning: a misspelled `destination`
that was silently ignored would only be noticed days later, with the files in
the wrong place.

Nothing is required. `gedad run` with no configuration works.

| Setting | Default | Notes |
|---|---|---|
| `name` | hostname | What phones see when they pick a destination. |
| `dest` | `<state_dir>/Photos` | Where received files land. |
| `state_dir` | `/var/lib/geda` as root, else the user config dir | Ledger, TLS identity, control socket. **Must persist.** |
| `listen` | `:47891` | TLS listen address. |
| `discovery_port` | `47890` | UDP discovery. |
| `advertise` | every local address | Only set behind NAT — see below. |
| `advertise_port` | the listen port | Only set behind a port mapping. |
| `discovery` / `mdns` | on | The L1–L2 layers; unicast keeps working without them. |
| `naming_template` | see AGENTS.md §3.6 | Validated at startup, then stored in the ledger. |
| `control_socket` | `<state_dir>/gedad.sock` | |
| `log_level` | `info` | |

### The state directory must survive updates

It holds the TLS identity key. A receiver that loses that key looks like a
different machine to every paired phone, and a pin mismatch is a hard failure
with no override — every device has to pair again (AGENTS.md §3.5).

### `advertise`, and when you need it

By default gedad tells peers about **every** address it has, VPN interfaces
included. That is what lets a phone on a remote network reach it over
WireGuard with no configuration at all, so leave it alone on a normal install.

Set it when the machine cannot see the address peers reach it on — a bridged
container sees only a private bridge address that nothing outside can dial.

## The control socket

`gedad pair` is not a second program reading the ledger. Pairing offers exist
only in the memory of the running daemon, so the command is a request to that
daemon over a Unix socket in the state directory.

The socket is the authorisation boundary: 0600, owned by the user the daemon
runs as. Reaching it means being that user or root on that machine. None of
this is exposed on the network — a pairing offer that anyone who can reach the
receiver could ask for would defeat the point of a QR code that requires
standing in front of it.

Inside a container, that is `docker compose exec gedad gedad pair`.

## Working on it

```
cd cli && go test -race ./...
```

`cli/` is a separate Go module that `replace`s `core/` to the sibling
directory, so `core/` stays a dependency-free library and the daemon's
dependencies — a QR encoder, terminal sizing — never reach it.

The phase gate is `scripts/verify-p3.sh`, which runs this image against curl on
another subnet.
