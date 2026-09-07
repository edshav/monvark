# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`monvark` is a BLAKE3 OpenCL GPU miner for **Monetarium**, forked from
`decred/gominer` and retargeted at `monetarium-node`. It lives inside the
`mon/` workspace (see `../CLAUDE.md` for the wider ecosystem) but is an
independent git repository with its own build. It mines solo only — CUDA,
stratum/pool support, ADL/NVML fan control and temperature reporting were all
removed in this stage; see "Removed in this stage" below.

**Naming.** The module path, binary name and config directory are all
`monvark` (`github.com/edshav/monvark`, `~/.monvark/`). This is a completed
rename, not a partial one — do not go looking for `gominer` remnants to clean
up. Internal identifiers still keep their `dcr*` heritage where they come from
imported Monetarium/Decred packages (`dcrutil`, `chainhash`, `blake256`,
`rpcclient`, `chaincfg` — used in `config.go`, `device.go`, `miner.go`,
`node.go` and `payout.go`); that is not in scope to change.

**Branches and remote.** `origin` is `git@github.com:edshav/monvark.git`,
matching the module path, and `main` is the only branch. The pristine upstream
fork point is the tag **`upstream-fork`** (`68791b0`), so
`git diff upstream-fork..main` is exactly the Monetarium delta — that tag is
what the old pinned `master` branch used to be for, and deleting it would lose
the only name for that commit. The old remote
(`edshav/monetarium-gominer`) still exists untouched but nothing points at it.

The repo also carries the upstream gominer version tags (`v0.2.0`…`v1.0.0`,
`release-v2.0.0`) **locally only** — their `go.mod` says
`github.com/decred/gominer`, so they must never be pushed to `monvark`. Push
branches and specific tags by name; never `git push --tags`.
`proxy.golang.org` caches published module versions permanently, so the first
semver tag pushed here is the point of no return for the module name.

## Build

`go build .` is the whole build. There are no build tags and nothing to
`go generate` — a single `device_opencl.go` provides the `Device` type
unconditionally, and the OpenCL library itself is loaded at run time (see
"The OpenCL binding" below), not linked at build time.

```sh
./run_tests.sh              # CI entrypoint: go test -v ./... + golangci-lint
go test -v ./...             # tests only
go test -run TestName ./util # a single test
golangci-lint run             # config in .golangci.yml, v2 schema
```

`CGO_ENABLED=0 go build .` works and is what CI cross-compiles with, for every
shipped platform, from one Ubuntu runner — no `#cgo` directive referencing a
GPU driver exists anywhere in the tree.

## Architecture

`main.go` → `loadConfig()` (`config.go`, all flag parsing) → build `devices`
→ `resolvePayout()` (`payout.go`) → `nodeStart()` + `WaitSynced()` (`node.go`)
→ `NewMiner(ctx, devices, workDone, payoutAddr)` (`miner.go`) → `m.Run(ctx)`,
which now returns an error. Benchmark mode (`-B`) skips `resolvePayout`,
`nodeStart` and `WaitSynced` entirely.

**Two work sources converge on one type.** `NewMiner` picks one based on
config, and both produce a `*work.Work` handed to every device via
`Device.SetWork()`:

- **benchmark** (`-B`) — a zero-valued `work.Work`; needs no node at all.
- **solo** (default) — websocket JSON-RPC to `monetarium-node`. One initial
  `GetWork` call for immediate work, then pushed `OnWork` callbacks from the
  `NotifyWork` subscription. Solutions go back via `GetWorkSubmit`.

`device.go` holds the backend-independent half of a device (the `Run`
wrapper, stats, host-side hash verification, `idleFor`); `device_opencl.go`
holds the `Device` struct itself and the `runDevice` loop.

**`--dutycycle` is the only throttle.** `runDevice` is otherwise a bare `for`
loop — enqueue kernel, wait, scan results, repeat — and at the default duty
cycle of 100 it still has no sleep at all. Below 100 it idles at the end of
each iteration for `idleFor(elapsed, dutyCycle)`, computed from the launch time
`runKernel` measured rather than from any configured size, so the throttle
holds at any intensity and on any card.

Two things about that placement are load-bearing. The idle sits *after* the
candidates are handled, so a found block is submitted immediately whatever the
throttle; and it is a `select` on `ctx.Done()`, not a `time.Sleep`, or Ctrl+C
would wait it out on every device before monvark could stop its node over RPC.

`--intensity` sets the work size (`2^i`), i.e. the length of one kernel
launch — it does *not* throttle the device or reduce heat.

**The default takes every device, not just the GPU.** `newMinerDevs`
enumerates with `cl.DeviceTypeAll` and, with no `--devices`, enables all of
them — the CPU and the integrated graphics included. On a machine someone is
sitting at that can look like a hang: a GPU has no preemption, so a kernel
queued on the display adapter stalls the compositor. The README documents
`--devices` as the fix; changing the default would be a behaviour change for
existing rigs, so raise it rather than quietly flipping it.

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

`blake3/` is a minimal BLAKE3 that accepts midstates — it exists specifically
to mirror what the kernels do, not as a general hash library.
`blake3/block_test.go` checks it against `lukechampine.com/blake3` over random
180-byte headers.

**BLAKE256 in `miner.go` is not leftover Decred code.** After DCP0011 the *proof
of work* hash is BLAKE3, but the *block identity* hash remains BLAKE256. The
`chainhash.Hash(blake256.Sum256(data[:180]))` line prints the block hash. Leave
it alone.

### Kernels and tuning

`blake3.cl` is the only hash kernel — do not edit it; the GPU/host agreement
test (`device_test.go`) exists specifically to catch a kernel that has drifted
from the host implementation. It is compiled into the binary via `go:embed`
(the `kernelSource` var in `device_opencl.go`), not read from disk, so the
binary runs correctly from any working directory. `kernel_test.go` asserts the
embed actually captured the kernel source. The build option passed to
`clBuildProgram` is `-D WORKSIZE=64`; a static check that a refactor did not
touch the mining path is confirming `blake3.cl` is unchanged and that build
option string is unchanged — see "Measuring hashrate" below for why a rate
comparison cannot serve as that check on this hardware.

`calibrate.go` sizes the work per launch to hit `--autocalibrate` milliseconds
per kernel execution, when the user has not set `--intensity` or `--worksize`
explicitly.

`monitor.go` serves a read-only JSON status endpoint when `--apilisten` is set
(default port 3333).

### The OpenCL binding (`cl/`)

The OpenCL library is resolved and loaded at run time through
[`purego`](https://github.com/ebitengine/purego) on first use (`cl.Load`,
called from `loadConfig`), not linked at build time — there is no `#cgo`
anywhere in this tree. `cl/cl.go` covers exactly **19 entry points**. Adding a
call to a 20th means two edits in that file: a bound function variable, and a
`reg("clWhatever", &whatever)` line inside `bind`. `cl/loader_unix.go` and
`cl/loader_windows.go` supply the platform-specific `openLibrary` — Windows
uses `LoadLibraryEx` with `LOAD_LIBRARY_SEARCH_SYSTEM32` specifically to keep a
co-extracted `OpenCL.dll` in the miner's own directory from being loaded ahead
of the real driver.

`purego` is pinned at **v0.10.2** deliberately, not just untouched: v0.11.0
declares `go 1.25`, which would raise this project's floor above the current
1.23 for no functional gain. Do not bump it without checking that constraint
first.

## Removed in this stage

CUDA, ADL/NVML fan control, GPU temperature reporting, and stratum/pool mining
were all deleted. There is no `--kernel`, `--temptarget`, `--experimental`,
`--pool`, `--pooluser` or `--poolpass` flag, and no build tag of any kind.
`nvidia-smi` (NVIDIA) and `rocm-smi` (AMD) cover fan and temperature reporting
outside this binary.

## Monetarium specifics

Dependencies are `monetarium-node` submodules at `v1.3.10`. Monetarium tags all
of its submodules together under one version with **no major-version path
suffixes**, so the dcrd `/v2`, `/v3`, `/v4`, `/v8` elements are gone
(`chaincfg/v3` → `chaincfg`, `rpcclient/v8` → `rpcclient`, …). Five files
import them: `config.go`, `device.go`, `miner.go`, `node.go`, `payout.go`.

`blake3pow` is **active since block 1** on Monetarium mainnet, and the block
header is the same 180 bytes as Decred's — which is why this BLAKE3-only miner
works unchanged.

**A node with `generate=1` refuses `getwork`** (`getwork polling is disallowed
while CPU mining is enabled`). Solo mining requires disabling CPU mining on the
node; `miningaddr` must stay, since the node needs it to build the coinbase.

## Node ownership

monvark starts and owns its own node; there is no mode for attaching to one it
did not configure, because a `getwork` template pays the `miningaddr` of
whichever node served it. `nodeStart` in `node.go` generates
`~/.monvark/mond.conf` at 0600 with the credentials and the loopback RPC port it
chose, and passes `--miningaddr` on the node's **command line**.

**That split is load-bearing, not stylistic.** The node picks uniformly at
random among its configured mining addresses
(`bgblktmplgenerator.go:728`), and go-flags *appends* command line occurrences to
config file entries rather than overriding them. An address that ever lands in
both places therefore pays a random fraction of blocks elsewhere. `mond.conf`
is checked for a `miningaddr` or `generate` key before being rewritten, and
their presence is an error rather than something to silently repair.

The default P2P port is left to whoever holds it: monvark test-binds it and
passes `--listen=:0` only when it is busy, so a machine already running a node
keeps working. Shutdown is the node's own `stop` RPC via `RawRequest`, not a
signal — `os.Interrupt` cannot be delivered through `Process.Signal` on Windows.

## Measuring hashrate

`Device.Status()` — what both the log lines and the status API report — is a
**cumulative average since the process started**, not an instantaneous rate.
A benchmark needs several minutes of runtime before its number means anything;
a run under a minute is dominated by ramp-up, not steady-state throughput.

**On this development machine the benchmark is thermally dominated, not code-
dominated**, which makes a rate comparison an unreliable regression check. The
same unmodified binary measured 1.75, 1.83 and 2.24 Gh/s across separate runs,
varying only by how long the GPU had idled beforehand — and every run's own
reported rate *declines* across its span as the card heat-soaks, so "read the
number at the end" is really reading the heat-soaked state. The design spec's
own figures for this card span 1.57 Gh/s (heat-soaked) to 2.34 Gh/s
(thermally controlled).

**Consequence:** a ±10% hashrate comparison across runs cannot detect a code
regression on this rig. To check that a refactor left the mining path alone,
use the static check instead — `blake3.cl` unchanged, `-D WORKSIZE=64`
unchanged (see "Kernels and tuning" above) — plus, for correctness rather than
speed, `device_test.go`'s GPU/host hash-agreement check. If a genuine rate
comparison is ever needed, run the old and new binaries back-to-back on the
same warmed-up (or same cold) device, never across sessions.

## Outstanding

- **`cl/loader_windows.go` has never been executed on Windows.** It is verified
  today only by cross-compilation and `go vet`, never by an actual run against a
  Windows OpenCL driver.
- **The Windows build is unsigned.** No code-signing certificate is budgeted, so
  SmartScreen and Defender treat it by category; the README documents the
  workaround.
- **`getblocktemplate` does not exist on `monetarium-node`.** The payee check
  therefore reads a block back with `GetBlockVerbose` after submitting it,
  rather than inspecting a template beforehand. It is evidence and a bug
  detector, not a defence against a substituted `mond`.
