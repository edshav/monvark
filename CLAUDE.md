# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A BLAKE3 GPU miner (OpenCL and CUDA) for **Monetarium**, forked from
`decred/gominer` and retargeted at `monetarium-node`. It lives inside the `mon/`
workspace (see `../CLAUDE.md` for the wider ecosystem) but is an independent git
repository with its own build.

The retarget was deliberately minimal: the module path is still
`github.com/decred/gominer`, the binary is still `gominer`, and the config still
lives in `~/.gominer/`. Do not "finish" the rebrand unless asked — the names are
load-bearing across `stratum/`, `util/`, `work/`, `blake3/`, `cl/`, `nvml/` and
`adl/` internal imports.

Git layout: `origin` is the private `edshav/monetarium-gominer`; the upstream
`decred/gominer` remote was deliberately removed. `master` is pinned at the
pristine upstream fork point (`68791b0`) so that
`git diff master..monetarium-retarget` is exactly the Monetarium delta. Work
happens on `monetarium-retarget`. To follow upstream again, add it under a
*different* name (`git remote add upstream …`), never as `origin`.

## Build: a GPU backend tag is mandatory

**`go build ./...` fails with `undefined: Device`.** This is expected, not a
broken checkout. The `Device` struct and its mining loop exist only inside the
backend files, each guarded by a mutually exclusive build tag:

| Command | File providing `Device` | Notes |
|---|---|---|
| `go build -tags opencl ./...` | `cldevice.go` | The default; what CI builds |
| `go build -tags opencladl ./...` | `cladldevice.go` | OpenCL + AMD ADL fan control (Windows/Linux only) |
| `go build -tags cuda ./...` | `cudevice.go` | Requires `go generate -tags cuda .` first |

The CUDA path runs `cuda_builder.go` (via the `go:generate` directive in
`cudevice.go`) to produce an intermediate library from `blake3.cu` before the
build works. `docs/cuda-manual-windows-build.md` covers the Windows case.

```sh
./run_tests.sh                              # CI entrypoint: tests + golangci-lint
go test -tags opencl ./...                  # tests only
go test -tags opencl -run TestName ./util   # a single test
golangci-lint run                           # config in .golangci.yml
```

Every `go` invocation needs the tag — tests and vet included. `.golangci.yml`
sets `build-tags: [opencl]`, uses `disable-all` plus an explicit enable list, and
disables revive's `var-naming` (the codebase uses underscores).

## Architecture

`main.go` → `loadConfig()` (`config.go`, all flag parsing) → `NewMiner(ctx)`
(`miner.go`) → `m.Run(ctx)`.

**Three work sources converge on one type.** `NewMiner` picks exactly one based
on config, and all three produce a `*work.Work` that is handed to every device
via `Device.SetWork()`:

- **benchmark** (`-B`) — a zero-valued `work.Work`; needs no node at all.
- **solo** (`--pool` empty) — websocket JSON-RPC to `monetarium-node`. One
  initial `GetWork` call for immediate work, then pushed `OnWork` callbacks from
  the `NotifyWork` subscription. Solutions go back via `GetWorkSubmit`.
- **stratum** (`--pool` set) — `stratum/` plus `getwork.go`; `workRefreshThread`
  polls `pool.PoolWork.NewWork` every 100ms.

`device.go` holds the tag-independent half of a device (the `Run` wrapper, stats,
`UpdateFanTemp`, host-side hash verification); the backend file holds the struct
and the `runDevice` loop. Splitting it this way is why the untagged build fails.

**The mining loop has no sleep.** `runDevice` is a bare `for` loop: enqueue
kernel, wait, scan results, repeat. The GPU runs at 100% duty cycle always.
`--intensity` sets the work size (`2^i`), i.e. the length of one kernel launch —
it does *not* throttle the device or reduce heat.

### Work data layout — the core domain fact

`getwork` returns **192 bytes**: the 180-byte block header plus 12 zero bytes of
padding (BLAKE3's block size is 64, and 180 rounds up to 192). Inside
`work.Work.Data`:

- `data[0:128]` — hashed once into a BLAKE3 midstate, cached per work item.
- `data[128:180]` — the tail the GPU mutates. Word offsets are named in
  `work/work.go`: `TimestampWord` 2, `Nonce0Word`–`Nonce3Word` 3–6, all indexed
  from byte 128.
- Host-side verification of a GPU candidate is
  `blake3.FinalBlock(d.midstate, data[128:180])` compared against the target via
  `standalone.HashToBig`.

`blake3/` is a minimal BLAKE3 that accepts midstates — it exists specifically to
mirror what the kernels do, not as a general hash library.

**BLAKE256 in `miner.go` is not leftover Decred code.** After DCP0011 the *proof
of work* hash is BLAKE3, but the *block identity* hash remains BLAKE256. The
`chainhash.Hash(blake256.Sum256(data[:180]))` line prints the block hash. Leave
it alone.

### Kernels and tuning

`blake3.cl` (OpenCL) and `blake3.cu` (CUDA) are the hash kernels. `--kernel`
defaults to the bare relative path `"blake3.cl"`, so it resolves against the
**current working directory** — running the binary from anywhere else fails at
kernel compile time. Pass an absolute path or `cd` first.

`calibrate.go` (built for everything except `cuda`) sizes the work per launch to
hit `--autocalibrate` milliseconds per kernel execution.

Fan control and temperature reads work only for `DeviceKindADL` (the
`opencladl` tag), `DeviceKindAMDGPU` (Linux sysfs) and `DeviceKindNVML`. On
macOS none apply and `--temptarget` fails outright with "fan control is not
supported on device kind".

`monitor.go` serves a read-only JSON status endpoint when `--apilisten` is set
(default port 3333).

## Monetarium specifics

Dependencies are `monetarium-node` submodules at `v1.3.9`. Monetarium tags all
of its submodules together under one version with **no major-version path
suffixes**, so the dcrd `/v2`, `/v3`, `/v4`, `/v8` elements are gone
(`chaincfg/v3` → `chaincfg`, `rpcclient/v8` → `rpcclient`, …). Only four files
import them: `config.go`, `device.go`, `miner.go`, `stratum/stratum.go`.

Solo-mining defaults point at the node: RPC cert under the `monetarium`
application data directory, ports **9509** mainnet / **19509** testnet /
**19956** simnet.

`blake3pow` is **active since block 1** on Monetarium mainnet, and the block
header is the same 180 bytes as Decred's — which is why this BLAKE3-only miner
works unchanged.

**A node with `generate=1` refuses `getwork`** (`getwork polling is disallowed
while CPU mining is enabled`). Solo mining requires disabling CPU mining on the
node; `miningaddr` must stay, since the node needs it to build the coinbase.

## Known stale spots

- `README.md` and `sample-gominer.conf` still describe `dcrd`, `~/.dcrd` and
  port 9109. They contradict the code after the retarget.
- `.github/workflows/go.yml` builds against Go 1.20/1.21, but `go.mod` now
  requires 1.23 (the Monetarium modules force it). The workflow needs updating
  before CI can pass.
- `stratum/` targets `dcrpool` semantics and no Monetarium pool exists. It is
  kept because retargeting it cost nothing — it only uses
  `wire.MaxBlockHeaderPayload`, `wire.BlockHeader` and `chaincfg.Params.PowLimit`.
