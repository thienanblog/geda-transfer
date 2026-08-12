# core

Pure Go. Everything Geda Transfer actually does lives here; `desktop/`, `cli/`,
and `docker/` are presentation layers over this package (AGENTS.md §2).

`core/` must never import Wails or any UI package, and must build with
`CGO_ENABLED=0` for every target — that is what lets one machine produce
macOS, Windows, Linux, amd64, and arm64 binaries for the NAS image.

## Packages

| Package | Purpose |
|---|---|
| `hash` | Streaming BLAKE3. Produces the full-file digest and the 1 MiB head digest in a single pass. |
| `store` | The SQLite ledger: paired devices, received files, reserved pair basenames, transfer history, settings. |

## Working on it

```
go test -race ./...
go test -run=xxx -bench=. ./hash/
```

Measured on an Apple M5 Pro, for reference when judging a change:

| Benchmark | Throughput |
|---|---|
| `BenchmarkHasher/2MiB` | ~2.9 GB/s |
| `BenchmarkHasher/64MiB` | ~7.2 GB/s |

Hashing is roughly two orders of magnitude faster than a Wi-Fi 6 link, so it is
not a bottleneck and should not be optimised further without a measurement
showing otherwise.
