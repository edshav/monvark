# Solo GPU Miner — Stage 1: The Miner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `gominer` into `monvark` — a single self-contained OpenCL-only
binary that builds with no build tags and no cgo, loads the OpenCL driver at
runtime, embeds its own kernel, and runs from any directory on Linux, macOS and
Windows.

**Architecture:** This stage is mostly deletion. Three of four GPU backends and
the entire pool path are removed, which collapses the build-tag polymorphism
that currently makes an untagged build fail. The one load-bearing addition is
`cl/`: a 4195-line cgo binding is replaced by a ~250-line function-pointer table
resolved through `purego` at first use, so the executable no longer links
against `libOpenCL` and starts even when no OpenCL is installed.

**Tech Stack:** Go 1.23, OpenCL 1.2 (kernel `blake3.cl`, untouched),
`github.com/ebitengine/purego` v0.10.2, `golang.org/x/sys/windows`,
`lukechampine.com/blake3` (test only).

**Spec:** `docs/superpowers/specs/2026-09-06-solo-gpu-miner-design.md`
— sections §2.4, §3.1–§3.6, §4, §5 steps 1–6, §7, §8. Read it alongside this
plan; the plan argues from it.

**Scope:** This is stage 1 of 2. It covers spec work-order steps 1–6 and the
two hash-verification items of §4. **Out of stage 1, deferred to stage 2:** first
run and the payout-address question, `mond` ownership and supervision, the sync
gate, the coinbase/payee assertion, work expiry, archive assembly and release
(spec §3.7, §5 steps 7–8). At the end of this stage the miner still expects a
node the user configured by hand, exactly as it does today. See
"Stage 2 preview" near the end of this plan for its shape and, more importantly,
for the stage 1 outcomes stage 2 has to live with.

## Global Constraints

Every task's requirements implicitly include this section. Values are copied
verbatim from the spec.

- **Go floor is 1.23** (`go.mod`). `purego` is pinned to **v0.10.2**, the newest
  release whose own `go.mod` says `go 1.18`; v0.11.0 requires `go 1.25.0` and
  must not be used.
- **The proof of work is frozen permanently.** `blake3.cl` is not edited — header
  and measurement table included (spec §8.5). Kernel optimization is out of
  scope (spec §6).
- **No `#cgo LDFLAGS` referencing a GPU driver may remain** anywhere in the tree
  (spec §3.2). The build must not require an SDK on the build machine.
- **OpenCL only.** No CUDA, no ADL, no NVML, no stratum (spec §2.4, §3.4, §3.6).
- **Multi-GPU is a requirement** (spec §1 requirement 5). `Nonce3Word` carries
  the device id and is load-bearing in solo mode; **three** bytes of it are
  spare, not four. Do not reuse the fourth (spec §3.6).
- **BLAKE256 in `miner.go` is not leftover Decred code.** The PoW hash is BLAKE3;
  the block identity hash is BLAKE256. Leave
  `chainhash.Hash(blake256.Sum256(data[:180]))` alone (repo `CLAUDE.md`).
- **Licensing:** the repository is GPL-3.0. Every
  `Copyright (c) ... The Decred developers` and `... The btcsuite developers`
  header stays in place; `LICENSE` is not modified; `cl/LICENSE` (MIT, Rain Liu)
  stays because the new `cl/` copies that binding's constant and error tables
  (spec §8.4, §8.5).
- **Names after task 6:** module `github.com/edshav/monvark`, binary `monvark`,
  config dir `~/.monvark/`. Internal package names and identifiers keep their
  `dcr*` heritage — deeper de-Decred renaming is out of scope (spec §6, §8.1).
- **Regression metric: benchmark hashrate on the reference rig**, re-measured
  after tasks 1, 3, 4 and 6 — the tasks that touch the mining path. See
  "Measuring hashrate" below; it is the only quantity a refactor can break
  silently (spec §4).
- **Financial software — correctness over speed.** Commit-message style is
  `package/path: concise description` (workspace `CLAUDE.md`).

### Measuring hashrate — read this before task 1

The reference rig for this plan is the development MacBook: **`DEV #2`,
AMD Radeon Pro 5300M Compute Engine**. The spec's 2.33 GH/s figure comes from a
thermally controlled run on this card (§2.2).

**The reported number is a cumulative average since process start**, not an
instantaneous rate — `device.go` computes
`allDiffOneShares * 0xFFFFFFFF / secondsElapsed` from `d.started`. It therefore
climbs for several minutes before converging. A measured 45-second run on this
rig reported 1.47 GH/s and was still rising. **A short run will report a false
regression.** Every parity check in this plan means:

```sh
go build -tags opencl -o /tmp/monvark-parity . && \
  ( /tmp/monvark-parity -B -D 2 & P=$!; sleep 330; kill $P ) 2>&1 | grep "DEV #2" | tail -3
```

Run for at least five minutes and take the **last** reading. A measured 5.5-minute
baseline run on this rig, on the tree as it stands at commit `c012a44`, converged
to **2.08-2.09 GH/s**; re-establish it yourself in task 1 rather than trusting
this number, since it moves with ambient temperature. Accept anything within 10%
of your own baseline; investigate anything below.

After task 3 the `-tags opencl` flag is gone and the command becomes
`go build -o /tmp/monvark-parity .`. After task 6 the binary is `monvark`.

---

## Deviations from the spec

Four points where the spec cannot be followed as written, or where following it
literally costs more than it buys. Each was verified against the code, not
inferred.

**1. The work order is reordered: deletion comes before the purego decision.**
The spec puts the purego spike at step 1 "because writing the cgo shim and then
discarding it costs more than the measurement" (§3.2). That reasoning is about
cgo-versus-purego, not about ordering against the deletions. `cladldevice.go`
and `cudevice.go` both call into `cl`, so measuring before deleting them means
the shim must satisfy call sites that are about to be deleted. This plan
therefore runs the deletions first (task 1), the tag/embed collapse second
(task 3), and writes the shim once, directly on purego, against the single
remaining call site (task 4). Nothing is thrown away: the Go-facing API of the
19-function table is identical whether its trampolines are purego or cgo, so a
purego failure costs only the loader files, not the table. Task 4 carries the
explicit fallback.

**2. `--kernel` is deleted, and so is the ability to run a modified kernel.**
This is the spec's own decision (§3.5) and it is repeated here because it is the
one deletion that removes a capability rather than dead weight.

**3. The Linux loader uses bare sonames, where spec §3.2 says "absolute
candidates on Linux".** The spec's stated reason for hardening the search is
library planting: "the distribution is an archive the user unzips into
`Downloads`: on Windows, `LoadLibraryW(L"OpenCL.dll")` searches the application
directory first". That reason is Windows-specific. `dlopen` does not search the
working directory, so on Linux absolute paths defend only against an
`LD_LIBRARY_PATH` the user set themselves, while adding a per-distribution
failure mode for every layout the list guesses wrong. The Windows hardening the
spec asks for is implemented exactly as written. Flagged rather than silently
changed — the owner can restore the absolute candidates if the threat model was
meant to be wider than the rationale given.

**4. Stage 2 will not be able to implement the coinbase assertion as specified —
recorded here so it is not discovered late.** Spec §3.7 step 3 and §4 "Payee
verification" both call for `getblocktemplate`. **That RPC does not exist on
`monetarium-node`.** Verified: `internal/rpcserver/rpcserver.go` has no
`"getblocktemplate"` entry in its handler map (`getwork`, `getmininginfo` and
`regentemplate` are the mining handlers present), `rpcclient` has no
`GetBlockTemplate` method, and the only repository-wide matches for the string
are two prose comments. Stage 2 will need a different payee check — the workable
one is `GetBlockVerbose(hash, true)` on a block we actually submitted, reading
`RawTx[0].Vout[].ScriptPubKey.Addresses` — plus passing `--miningaddr` to `mond`
on its command line so the address is our own argument rather than a file. **No
action in stage 1.** It is written down because it changes stage 2's scope and
because the spec calls this "the only point in the system where the payee is ever
verified".

---

## File Structure

Where things end up at the end of stage 1.

**Deleted outright**

```
cladldevice.go            80% copy of cldevice.go (spec §3.1)
cudevice.go               OpenCL covers nVidia (spec §2.4)
cuda_builder.go           removes nvcc from the build
blake3.cu                 the CUDA kernel
decred.h                  CUDA header
getwork.go                stratum work source
adl/  nvml/  stratum/     whole packages
notify/                   a package main fake stratum server, debug aid for stratum
docs/cuda-manual-windows-build.md
cl/*.go except the new files below, plus cl/cl.h
```

**Rewritten**

```
cl/cl.go              types, constants, the bound entry points, Load
cl/loader_unix.go     library candidates + dlopen
cl/loader_windows.go  candidates + LoadLibraryEx, hardened search
```

**Renamed**

```
cldevice.go  →  device_opencl.go     build tag removed
```

**Modified**

```
device.go     fan control, temperature and their constants removed
calibrate.go  build tag removed
config.go     temptarget, experimental, CUDA and pool flags removed; --opencl-lib added
miner.go      stratum removed; one work source plus benchmark
monitor.go    fan, temperature and pool fields removed from the status JSON
log.go        poolLog and the stratum logger removed
main.go       cl.Load wired in
go.mod        purego and x/sys direct; module path changes in task 6
```

**Untouched**

```
blake3.cl     the kernel, now embedded (spec §8.5: not edited)
blake3/       gains block_test.go in task 2
work/  util/  signal.go  signal_syscall.go  version.go
LICENSE  cl/LICENSE
```

---

## Task 1: Remove CUDA, ADL, NVML, stratum, fan control and temperature

Spec §5 step 2, §3.4, §3.6. Pure deletion. Nothing here should change behaviour
on the OpenCL path, which is what the parity check at the end proves.

**Files:**
- Delete: `cudevice.go`, `cuda_builder.go`, `cladldevice.go`, `blake3.cu`,
  `decred.h`, `getwork.go`, `docs/cuda-manual-windows-build.md`, `adl/`,
  `nvml/`, `stratum/`, `notify/`
- Modify: `device.go`, `cldevice.go`, `config.go`, `miner.go`, `monitor.go`,
  `log.go`
- Test: none added — the deliverable is proven by the build plus the parity run

**Interfaces:**
- Consumes: nothing (first task)
- Produces:
  - `func (d *Device) Status() float64` — was
    `(float64, uint32, uint32)`; the fan and temperature returns are gone
  - `func (m *Miner) Status() (valid, rejected, total uint64)` — was
    `(uint64, uint64, uint64, uint64, float64)`; stale shares and pool utility
    are gone
  - `Device` loses the fields `fanPercent`, `temperature`, `fanControlActive`,
    `fanControlLastTemp`, `fanControlLastFanPercent`, `fanTempActive`, `kind`,
    `tempTarget`
  - `Miner` loses the fields `pool`, `needsWorkRefresh`, `staleShares`
  - `config` loses `Experimental`, `TempTarget`, `TempTargetInts`,
    `CudaGridSize`, `CudaGridSizeInts`, `CudaThreadCount`,
    `CudaThreadCountInts`, `Pool`, `PoolUser`, `PoolPassword`

- [ ] **Step 1: Record the baseline hashrate**

This is the number every later parity check compares against. Do it before
touching anything.

```sh
go build -tags opencl -o /tmp/monvark-baseline . && \
  ( /tmp/monvark-baseline -B -D 2 & P=$!; sleep 330; kill $P ) 2>&1 \
  | grep "DEV #2" | tail -3
```

Put the last reading in this task's commit message — it is one number, every
parity step in this plan quotes it back, and a tracked file for it would be one
more thing to keep in sync.

Expected: a reading in the region of 2.0-2.4 GH/s. If the card is not `DEV #2`
on your machine, run `go run -tags opencl . -l` first and use the index of the
discrete GPU.

- [ ] **Step 2: Delete the backend files and the dead packages**

```sh
git rm -r --quiet \
  cudevice.go cuda_builder.go cladldevice.go blake3.cu decred.h getwork.go \
  docs/cuda-manual-windows-build.md adl nvml stratum notify
```

`notify/` is a `package main` fake stratum server kept as a debug aid for the
pool path; it goes with `stratum/`. `getwork.go` contains only `GetPoolWork` and
`GetPoolWorkSubmit`.

- [ ] **Step 3: Strip fan control and temperature from `device.go`**

Delete the whole constant block and replace it with just the two device-type
strings that `getDeviceInfo` still uses:

```go
// Device type strings as reported by CL_DEVICE_TYPE.
const (
	DeviceTypeCPU = "CPU"
	DeviceTypeGPU = "GPU"
)
```

Delete `fanControl()` (lines 173-325), `fanControlSupported()` (327-336),
`UpdateFanTemp()` (411-426) and the `deviceLibraryInitialized` variable, which
existed only for the CUDA and ADL backends. Reduce `Status` and `PrintStats` to:

```go
func (d *Device) PrintStats() {
	minrLog.Infof("DEV #%d (%s) %v", d.index, d.deviceName,
		util.FormatHashRate(d.Status()))
}

// Status returns the average hash rate of the device since it was started.
func (d *Device) Status() float64 {
	secondsElapsed := uint32(time.Now().Unix()) - d.started
	if secondsElapsed == 0 {
		return 0
	}

	// allDiffOneShares is incremented by foundCandidate under this lock, and
	// Status is called both from the stats goroutine and from the status API,
	// so the read has to take it too.  The previous code read it unlocked from
	// both.
	d.Lock()
	shares := d.allDiffOneShares
	d.Unlock()

	const diffOneShareHashesAvg = float64(0x00000000FFFFFFFF)
	return (diffOneShareHashesAvg * float64(shares)) / float64(secondsElapsed)
}
```

Note that `Status` now takes the lock and `PrintStats` no longer holds it. Go
mutexes are not reentrant, so leaving the old `d.Lock()` in `PrintStats` while
`Status` also locks would deadlock the stats goroutine on its first tick.

Drop `sync/atomic` from the imports; nothing in the file uses it any more.

- [ ] **Step 4: Strip the sysfs and fan machinery from `cldevice.go`**

Delete `amdgpuFanPercentToValue`, `amdgpuFanPermissionsValid`,
`amdgpuGetSysfsPath`, `fanControlSet`, `determineDeviceKind`, `deviceStats`,
`deviceStatsReadSysfsEntry` and `deviceStatsWriteSysfsEntry` — lines 48-163 and
265-335. Remove the eight fields listed in the Interfaces block from the
`Device` struct, and delete these two blocks from `NewDevice` (the device-kind
probe at lines 541-554 and the temperature-target validation at 556-596):

```go
	// Determine the device/driver kind
	d.kind = determineDeviceKind(d.index, d.deviceType)
	... through ...
	if fanControlNotWorking {
		d.tempTarget = 0
		d.fanControlActive = false
	}
```

Also delete `fanPercent`, `temperature` and `tempTarget` from the `&Device{...}`
literal at line 397. Drop the now-unused imports `bufio`, `runtime`, `strconv`,
`strings` and `sync/atomic`. Delete the trailing `XXX` comment in `Release()`
about the AMDGPU driver taking back fan control.

- [ ] **Step 5: Strip the dead flags from `config.go`**

Delete the struct fields listed in the Interfaces block, the
`defaultCudaThreadCount` constant, the `minTempTarget` and `maxTempTarget`
variables, `CudaThreadCount: defaultCudaThreadCount` from the default config
literal, the temperature-target parsing block (lines 476-535), and the two
`commaListToInts` calls for CUDA (lines 590-600). `commaListToInts` itself then
has no callers — delete it too, along with the `time` import, whose only use was
the three-second sleep in the `--experimental` warning.

- [ ] **Step 6: Reduce `miner.go` to one work source plus benchmark**

Delete `newStratum`, `workRefreshThread`, the `pool` and `needsWorkRefresh`
fields, and the `staleShares` counter. `NewMiner`'s switch collapses:

```go
	var m *Miner
	if cfg.Benchmark {
		m = newBenchmarkMiner(devices)
	} else {
		m, err = newSoloMiner(ctx, devices)
	}
	if err != nil {
		return nil, err
	}
```

The `if cfg.Pool == ""` guard around the initial `GetWork` call goes away — solo
is now the only non-benchmark path. `workSubmitThread` loses its `else` branch
and keeps only the solo body. `Run` loses the `else if m.pool != nil` branch that
started `workRefreshThread`. `printStatsThread` loses the pool branch, the
`d.UpdateFanTemp()` and `d.fanControl()` calls, and -- this one is easy to miss
and breaks the build -- the five-value destructuring of `m.Status()`:

```go
		if !cfg.Benchmark {
			valid, rejected, total := m.Status()
			minrLog.Infof("Global stats: Accepted: %v, Rejected: %v, Total: %v",
				valid, rejected, total)
		}

		for _, d := range m.devices {
			d.PrintStats()
		}
```

`m.needsWorkRefresh` disappears from this function's `select` too, leaving
`ctx.Done()` and the ticker.

`Status` becomes:

```go
// Status returns the miner's accepted, rejected and total share counts.
func (m *Miner) Status() (uint64, uint64, uint64) {
	valid := atomic.LoadUint64(&m.validShares)
	rejected := atomic.LoadUint64(&m.invalidShares)
	return valid, rejected, valid + rejected
}
```

Drop the `errors` and `stratum` imports. **Do not touch** the
`chainhash.Hash(blake256.Sum256(data[:180]))` line — that is the block identity
hash, not leftover Decred code.

- [ ] **Step 7: Strip pool, fan and temperature from `monitor.go` and `log.go`**

In `monitor.go` delete the `PoolStatus` type, the `Pool` field, the
`StaleShares` and `SharesPerMinute` fields of `MinerStatus`, and the
`FanPercent` and `Temperature` fields of `DeviceStatus`. The two loops become:

```go
	if !cfg.Benchmark {
		valid, invalid, total := m.Status()
		ms.ValidShares = valid
		ms.InvalidShares = invalid
		ms.TotalShares = total
	}

	for _, d := range m.devices {
		hashRate := d.Status()
		ms.Devices = append(ms.Devices, &DeviceStatus{
			Index:             d.index,
			DeviceName:        d.deviceName,
			DeviceType:        d.deviceType,
			HashRate:          hashRate,
			HashRateFormatted: util.FormatHashRate(hashRate),
			Started:           d.started,
		})
	}
```

In `log.go` delete the `stratum` import, the whole `init()` that calls
`stratum.UseLogger(poolLog)`, the `poolLog` variable and the `"POOL"` entry in
`subsystemLoggers`.

- [ ] **Step 8: Tidy the module and build**

```sh
go mod tidy
go build -tags opencl ./...
go vet -tags opencl ./...
```

Expected: all three succeed. `go mod tidy` should drop
`github.com/barnex/cuda5`, `github.com/davecgh/go-spew` and
`github.com/decred/go-socks` from the direct requirements if nothing else uses
them — check the diff of `go.mod` and do not hand-edit it.

- [ ] **Step 9: Verify parity**

```sh
go build -tags opencl -o /tmp/monvark-t1 . && \
  ( /tmp/monvark-t1 -B -D 2 & P=$!; sleep 330; kill $P ) 2>&1 \
  | grep "DEV #2" | tail -3
```

Expected: within 10% of the task 1 step 1 baseline. This is deletion only, so a
real change here means something on the hot path was removed by accident.

- [ ] **Step 10: Commit**

```sh
git add -A
git commit -m "miner: Remove CUDA, ADL, NVML, stratum and fan control.

OpenCL covers both AMD and nVidia, so the CUDA backend, the ADL fan-control
backend and their build machinery are removed along with the stratum pool
path.  Fan control and temperature reads go with them: the implementation was
Linux-only sysfs, so the fields read zero on Windows and macOS for every
vendor, and nvidia-smi and rocm-smi do the job properly.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BFrJkoVTnN2SucvajpiSsX"
```

---

## Task 2: Golden test for the host BLAKE3 against an independent implementation

Spec §4, "Host side, independently". This lands before the `cl/` rewrite on
purpose: it is the net that catches a host-side hashing mistake while later
tasks move the GPU code around, and it needs no GPU to run.

The spec warns that `monetarium-stratum/cmd/gpuminer/midstate_test.go` is **not**
reusable as the golden vector — it ports a different kernel's compress function,
computes its expected value at runtime rather than fixing one, and depends on a
module absent from our `go.mod`. Test against `lukechampine.com/blake3` instead,
which is already in `go.sum` as an indirect dependency, so this adds no new
module to the build.

**Why this equivalence holds:** a 192-byte work item is hashed over its first
180 bytes. BLAKE3's chunk size is 1024, so all 180 bytes are chunk 0, which is
also the root. That is exactly two full 64-byte blocks plus a 52-byte final
block — `Block(IV, d[0:64], FlagChunkStart)`, `Block(ms, d[64:128], 0)`,
`FinalBlock(ms, d[128:180])` — and it must equal the plain BLAKE3-256 of
`d[:180]`. This was run against the real packages before being written down here;
it passes.

**Files:**
- Create: `blake3/block_test.go`
- Modify: `go.mod` (promotes `lukechampine.com/blake3` to a direct test
  requirement)
- Test: `blake3/block_test.go`

**Interfaces:**
- Consumes: `blake3.IV`, `blake3.FlagChunkStart`,
  `blake3.Block(midstate [8]uint32, b []byte, flags uint32) [8]uint32`,
  `blake3.FinalBlock(midstate [8]uint32, b []byte) [32]byte` — all already
  exported by the package
- Produces: nothing consumed by later tasks

- [ ] **Step 1: Write the failing test**

Create `blake3/block_test.go`:

```go
// Copyright (c) 2026 The Decred developers.

package blake3

import (
	"bytes"
	"math/rand"
	"testing"

	ref "lukechampine.com/blake3"
)

// hashWork reproduces the way the miner hashes a work item: the first 128 bytes
// are folded into a midstate and the 52-byte tail is hashed as the final block.
// This mirrors device.go's updateCurrentWork and foundCandidate.
func hashWork(data *[192]byte) [32]byte {
	midstate := Block(IV, data[0:64], FlagChunkStart)
	midstate = Block(midstate, data[64:128], 0)
	return FinalBlock(midstate, data[128:180])
}

// TestFinalBlockMatchesReference verifies the midstate-based BLAKE3 in this
// package against an independent implementation.  The 180 bytes of a block
// header fit in a single BLAKE3 chunk, which is therefore also the root, so the
// midstate construction must agree with a plain BLAKE3-256 over the same bytes.
func TestFinalBlockMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 256; i++ {
		var data [192]byte
		rng.Read(data[:])

		if got, want := hashWork(&data), ref.Sum256(data[:180]); !bytes.Equal(got[:], want[:]) {
			t.Fatalf("random vector %d mismatch:\n data %x\n got  %x\n want %x",
				i, data[:180], got, want)
		}
	}
}
```

There is deliberately no second test pinning a fixed vector. BLAKE3 is a frozen
specification, so the reference cannot drift out from under this, and a vector
whose expected value was produced by running these same two implementations
would be a snapshot of current behaviour rather than an independent oracle. The
random vectors are seeded, so a failure is reproducible without one.

- [ ] **Step 2: Run the test to verify it fails to build**

```sh
go test -tags opencl ./blake3/
```

Expected: FAIL — `no required module provides package lukechampine.com/blake3`
(it is present in `go.sum` but only as an indirect requirement).

- [ ] **Step 3: Promote the reference implementation to a direct requirement**

```sh
go get lukechampine.com/blake3@v1.3.0
go mod tidy
```

No new module is downloaded — this only moves the existing entry out of the
indirect block.

- [ ] **Step 4: Run the test to verify it passes**

```sh
go test -tags opencl -v ./blake3/
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add blake3/block_test.go go.mod go.sum
git commit -m "blake3: Test the midstate hash against a reference.

The 180 bytes hashed for proof of work fit in one BLAKE3 chunk, which is also
the root, so the midstate construction must agree with a plain BLAKE3-256 over
the same bytes.  Checking that against lukechampine.com/blake3 tests this
package against something other than itself.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BFrJkoVTnN2SucvajpiSsX"
```

---

## Task 3: Drop the build tags and embed the kernel

Spec §5 step 4, §3.3, §3.5. With one backend left, the build-tag polymorphism
has nothing to select between, and `undefined: Device` on an untagged build
stops being expected behaviour. A `Device` interface is deliberately **not**
introduced: a single implementation does not need the abstraction and the hot
loop should not pay for dispatch (spec §3.3).

The kernel moves into the binary in the same task, because both changes serve
the same goal — an executable that runs from wherever it was unpacked — and
neither is separately reviewable in a useful way.

**Files:**
- Rename: `cldevice.go` → `device_opencl.go`
- Modify: `device_opencl.go`, `calibrate.go`, `config.go`, `.golangci.yml`,
  `run_tests.sh`
- Create: `kernel_test.go`
- Test: `kernel_test.go`

**Interfaces:**
- Consumes: task 1's trimmed `Device` struct and `NewDevice`
- Produces:
  - `var kernelSource string` — the contents of `blake3.cl`, embedded at compile
    time. Task 4 passes this to `cl.CreateProgramWithSource`.
  - `config` loses the `ClKernel` field; `defaultClKernel` and
    `loadProgramSource` no longer exist

- [ ] **Step 1: Write the failing test**

Create `kernel_test.go`:

```go
// Copyright (c) 2026 The Decred developers.

package main

import (
	"strings"
	"testing"
)

// TestKernelSourceEmbedded ensures the OpenCL kernel is compiled into the
// binary.  The miner is distributed as a bare executable, so a kernel that has
// to be found on disk at run time fails to start on every machine except the
// one it was built on.
func TestKernelSourceEmbedded(t *testing.T) {
	if len(kernelSource) == 0 {
		t.Fatal("kernel source is empty; the go:embed directive is not working")
	}
	if !strings.Contains(kernelSource, "__kernel void\nsearch(") {
		t.Fatalf("embedded source does not declare the search kernel (%d bytes)",
			len(kernelSource))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```sh
go test -tags opencl -run TestKernelSourceEmbedded ./
```

Expected: FAIL — `undefined: kernelSource`.

- [ ] **Step 3: Embed the kernel and delete the loader**

Rename the file first so the rest of the edits land in its final home:

```sh
git mv cldevice.go device_opencl.go
```

In `device_opencl.go`, delete the two build-constraint lines at the top
(`//go:build opencl && !cuda && !opencladl` and its `// +build` twin) and add
the embed near the imports:

```go
import (
	_ "embed"
	...
)

// kernelSource is the OpenCL kernel, compiled into the binary.  --kernel used
// to point at this file on disk and defaulted to a bare relative path, so the
// miner only ran from its own build directory.
//
//go:embed blake3.cl
var kernelSource string
```

Delete `loadProgramSource` entirely (lines 165-189) along with the `bytes` and
`io` imports, and replace its call in `NewDevice`:

```go
	// Create the program from the embedded kernel source.
	progSrc := [][]byte{[]byte(kernelSource)}
	progSize := []cl.CL_size_t{cl.CL_size_t(len(kernelSource))}
	d.program = cl.CLCreateProgramWithSource(d.context, 1, progSrc, progSize, &status)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLCreateProgramWithSource")
	}
```

This keeps the old `cl` signature; task 4 simplifies it. Delete the
`// Load kernel source.` block that preceded it and its error handling.

- [ ] **Step 4: Drop the remaining build tag and the `--kernel` flag**

In `calibrate.go` delete the `//go:build !cuda` and `// +build !cuda` lines.

In `config.go` delete the `defaultClKernel` constant, the `ClKernel` struct
field with its `short:"k" long:"kernel"` tag, and `ClKernel: defaultClKernel`
from the default config literal.

- [ ] **Step 5: Take the tag out of the tooling**

In `.golangci.yml` delete the `build-tags:` block under `run:`, leaving:

```yaml
run:
  deadline: 10m
```

In `run_tests.sh` change the test line to:

```sh
go test -v ./...
```

- [ ] **Step 6: Verify the untagged build and the test**

```sh
go build ./...
go test -run TestKernelSourceEmbedded -v ./
go vet ./...
```

Expected: all succeed with **no `-tags opencl`**. This is the point at which the
repo's long-standing "`go build ./...` fails with `undefined: Device`" note stops
being true.

- [ ] **Step 7: Verify it runs from an unrelated directory**

This is the behaviour the task exists for.

```sh
go build -o /tmp/monvark-t3 . && (cd / && /tmp/monvark-t3 -l)
```

Expected: the device list prints. Before this task the same command failed at
kernel compile time because `blake3.cl` was resolved against the working
directory.

- [ ] **Step 8: Verify parity**

```sh
( /tmp/monvark-t3 -B -D 2 & P=$!; sleep 330; kill $P ) 2>&1 | grep "DEV #2" | tail -3
```

Expected: within 10% of the task 1 baseline.

- [ ] **Step 9: Commit**

```sh
git add -A
git commit -m "miner: Drop build tags and embed the kernel.

With OpenCL the only backend, the mutually exclusive build tags have nothing
left to select between, so cldevice.go becomes device_opencl.go and an
untagged build works.  The kernel moves into the binary with go:embed and
--kernel is removed: it defaulted to a bare relative path, so the miner only
ran from its own build directory, which is a blocker for a downloaded archive.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BFrJkoVTnN2SucvajpiSsX"
```

---

## Task 4: Replace `cl/` with a runtime-loaded, cgo-free binding

Spec §5 steps 1 and 3, §3.2 — the load-bearing part of this stage.

**The principle: no driver dependency at link time.** Today four `#cgo LDFLAGS`
directives bind the executable to a library being present *at startup* rather
than at first use, so a miner built with `-lOpenCL` will not launch on a machine
without OpenCL — not even to print a useful error. `cl/` is a complete 4195-line
binding of which this miner calls exactly **19 functions**, verified across
`device_opencl.go` and `calibrate.go`.

**The replacement is an internal detail of one binary, not a public binding.**
purego passes a Go slice as its data pointer (a nil slice becomes NULL, which is
OpenCL's own "no value" convention), null-terminates a Go `string` into `char*`
itself, and keeps pointer arguments alive across the call. Checked in purego
v0.10.2's `func.go`: `reflect.Slice` and `reflect.Ptr` both reach
`addInt(v.Pointer())` under the comment "There is no need to keepAlive this
pointer separately", and `reflect.String` goes through `strings.CString`. So
thirteen of the nineteen entry points need no Go code at all beyond their
declaration — the function variable *is* the API — and the six that remain are
the ones doing real work: two size-probe reads, the `char**` program source, and
three enqueue calls whose event parameters are always nil here.

The package is therefore named in idiomatic Go (`cl.DeviceID`, `cl.Success`)
rather than carrying the C spelling through. Every underscore identifier in the
repository lives in the files this task deletes — verified — so task 7 can drop
the `revive` `var-naming` exception that exists solely for them.

**purego is the first choice and the fallback is written into this task.**
purego's Tier 1 covers Linux, macOS and Windows on amd64 and arm64 with no
`CGO_ENABLED=1` requirement, and the two risks the spec names are addressed: our
19 functions pass no structs by value (no image APIs, so `cl_image_format` never
appears), and the blocking-read concern gets a real multi-device measurement in
step 8. `purego.Dlopen` is not built on Windows, which is why the Windows loader
uses `windows.LoadLibraryEx` — also exactly what the spec's hardening requires.

**Files:**
- Delete: `cl/buffer.go`, `cl/cgo_flags.go`, `cl/cl.go`, `cl/cl.h`,
  `cl/context.go`, `cl/device.go`, `cl/event.go`, `cl/event11.go`,
  `cl/image.go`, `cl/image11.go`, `cl/kernel.go`, `cl/kernel1x.go`,
  `cl/memory.go`, `cl/platform.go`, `cl/program.go`, `cl/program11.go`,
  `cl/queue.go`, `cl/queue1x.go`, `cl/sampler.go`, `cl/sampler1x.go`
- Create: `cl/cl.go`, `cl/loader_unix.go`, `cl/loader_windows.go`,
  `cl/cl_test.go`
- Modify: `device_opencl.go`, `calibrate.go`, `config.go`, `go.mod`
- Keep: `cl/LICENSE` — the error names are copied from the binding being replaced
- Test: `cl/cl_test.go`

**Interfaces:**
- Consumes: `kernelSource` (task 3)
- Produces, for `device_opencl.go` and `calibrate.go`:

```go
// Opaque handles, distinct types so a context cannot be passed as a queue.
type PlatformID uintptr
type DeviceID uintptr
type Context uintptr
type Queue uintptr
type Mem uintptr
type Program uintptr
type Kernel uintptr

type DeviceType uint64
type DeviceInfo uint32
type MemFlags uint64

const (
	Success         int32 = 0
	DeviceNotFound  int32 = -1
	InvalidValue    int32 = -30
)
const (
	DeviceTypeCPU DeviceType = 1 << 1
	DeviceTypeGPU DeviceType = 1 << 2
	DeviceTypeAll DeviceType = 0xFFFFFFFF
)
const (
	DeviceName DeviceInfo = 0x102B
	DeviceTypeParam DeviceInfo = 0x1000
)
const MemReadWrite MemFlags = 1 << 0

func Load(path string) error
func ErrorName(status int32) string

// Bound entry points, callable directly.  nil until Load succeeds.
var GetPlatformIDs func(numEntries uint32, platforms []PlatformID, numPlatforms *uint32) int32
var GetDeviceIDs func(platform PlatformID, deviceType DeviceType, numEntries uint32,
	devices []DeviceID, numDevices *uint32) int32
var CreateContext func(properties *uintptr, numDevices uint32, devices []DeviceID,
	notify, userData uintptr, errcode *int32) Context
var CreateCommandQueue func(ctx Context, device DeviceID, properties uint64, errcode *int32) Queue
var CreateBuffer func(ctx Context, flags MemFlags, size uint64, hostPtr unsafe.Pointer,
	errcode *int32) Mem
var BuildProgram func(program Program, numDevices uint32, devices []DeviceID,
	options string, notify, userData uintptr) int32
var CreateKernel func(program Program, name string, errcode *int32) Kernel
var SetKernelArg func(kernel Kernel, index uint32, size uint64, value unsafe.Pointer) int32
var ReleaseMemObject func(mem Mem) int32
var ReleaseKernel func(kernel Kernel) int32
var ReleaseProgram func(program Program) int32
var ReleaseCommandQueue func(queue Queue) int32
var ReleaseContext func(ctx Context) int32

// The six that need Go code.
func DeviceInfoString(device DeviceID, name DeviceInfo) (string, int32)
func DeviceInfoUint64(device DeviceID, name DeviceInfo) (uint64, int32)
func CreateProgramWithSource(ctx Context, source string, errcode *int32) Program
func ProgramBuildLog(program Program, device DeviceID) (string, int32)
func EnqueueNDRangeKernel(queue Queue, kernel Kernel, globalWorkSize, localWorkSize uint64) int32
func EnqueueReadBuffer(queue Queue, buffer Mem, blocking bool, offset, size uint64,
	ptr unsafe.Pointer) int32
func EnqueueWriteBuffer(queue Queue, buffer Mem, blocking bool, offset, size uint64,
	ptr unsafe.Pointer) int32
```

  `EnqueueNDRangeKernel` takes scalars because both call sites pass work
  dimension 1, a nil global offset and one-element arrays. Sizes are `uint64`
  because C `size_t` is 8 bytes on every platform this miner ships for; a 32-bit
  target would need a build-tagged alias, and there is no such target.

- [ ] **Step 1: Add the dependency**

```sh
go get github.com/ebitengine/purego@v0.10.2
```

Pin v0.10.2 exactly. v0.11.0 declares `go 1.25.0` in its own `go.mod` and would
drag the project's floor up from 1.23.

- [ ] **Step 2: Write the failing test**

Create `cl/cl_test.go`:

```go
// Copyright (c) 2026 The Decred developers.

package cl

import (
	"strings"
	"testing"
)

// TestLoadMissingLibraryReportsPath verifies that a library which cannot be
// resolved produces an error naming what was tried and pointing at the
// override, rather than a panic or a bare "not found".  This is the error most
// support reports will quote.
func TestLoadMissingLibraryReportsPath(t *testing.T) {
	const bogus = "/nonexistent/libOpenCL.so.1"

	err := Load(bogus)
	if err == nil {
		t.Fatal("expected an error for a library that does not exist")
	}
	if !strings.Contains(err.Error(), bogus) {
		t.Fatalf("error does not name the library that was tried: %v", err)
	}
	if !strings.Contains(err.Error(), "--opencl-lib") {
		t.Fatalf("error does not point at the override: %v", err)
	}
}

// TestErrorNameFallback checks that an unrecognised status still renders as
// something a user can quote.
func TestErrorNameFallback(t *testing.T) {
	if got := ErrorName(-30); got != "CL_INVALID_VALUE" {
		t.Fatalf("ErrorName(-30) = %q, want CL_INVALID_VALUE", got)
	}
	if got := ErrorName(-9999); !strings.Contains(got, "9999") {
		t.Fatalf("ErrorName(-9999) = %q, want the number in it", got)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

```sh
go test ./cl/
```

Expected: FAIL — `undefined: Load`, `undefined: ErrorName`.

- [ ] **Step 4: Delete the old binding**

```sh
git rm --quiet cl/buffer.go cl/cgo_flags.go cl/cl.go cl/cl.h cl/context.go \
  cl/device.go cl/event.go cl/event11.go cl/image.go cl/image11.go \
  cl/kernel.go cl/kernel1x.go cl/memory.go cl/platform.go cl/program.go \
  cl/program11.go cl/queue.go cl/queue1x.go cl/sampler.go cl/sampler1x.go
```

`cl/LICENSE` stays.

- [ ] **Step 5: Write `cl/cl.go`**

One file: the types and constants from the Interfaces block above, the bound
function variables, the six real functions, `ErrorName`, and `Load`. The parts
that are not mechanical:

The **raw declarations must use explicitly sized types**. purego maps Go types
to C types by width, and `int` and `uint` are ambiguous, so neither may appear
in a bound signature. The handle and enum types above are all defined over sized
types, so they are safe to use directly.

`ErrorName` is a map rather than the two index-arithmetic arrays the old binding
used, so entries can be added one line at a time:

```go
// errorNames maps an OpenCL status to its symbolic name.  Copied from the go-cl
// binding this package replaces; see LICENSE.
var errorNames = map[int32]string{
	0: "CL_SUCCESS", -1: "CL_DEVICE_NOT_FOUND", -2: "CL_DEVICE_NOT_AVAILABLE",
	-3: "CL_COMPILER_NOT_AVAILABLE", -4: "CL_MEM_OBJECT_ALLOCATION_FAILURE",
	-5: "CL_OUT_OF_RESOURCES", -6: "CL_OUT_OF_HOST_MEMORY",
	-11: "CL_BUILD_PROGRAM_FAILURE", -12: "CL_MAP_FAILURE",
	-30: "CL_INVALID_VALUE", -31: "CL_INVALID_DEVICE_TYPE",
	-32: "CL_INVALID_PLATFORM", -33: "CL_INVALID_DEVICE",
	-34: "CL_INVALID_CONTEXT", -35: "CL_INVALID_QUEUE_PROPERTIES",
	-36: "CL_INVALID_COMMAND_QUEUE", -38: "CL_INVALID_MEM_OBJECT",
	-42: "CL_INVALID_BINARY", -43: "CL_INVALID_BUILD_OPTIONS",
	-44: "CL_INVALID_PROGRAM", -45: "CL_INVALID_PROGRAM_EXECUTABLE",
	-46: "CL_INVALID_KERNEL_NAME", -47: "CL_INVALID_KERNEL_DEFINITION",
	-48: "CL_INVALID_KERNEL", -49: "CL_INVALID_ARG_INDEX",
	-50: "CL_INVALID_ARG_VALUE", -51: "CL_INVALID_ARG_SIZE",
	-52: "CL_INVALID_KERNEL_ARGS", -53: "CL_INVALID_WORK_DIMENSION",
	-54: "CL_INVALID_WORK_GROUP_SIZE", -55: "CL_INVALID_WORK_ITEM_SIZE",
	-56: "CL_INVALID_GLOBAL_OFFSET", -58: "CL_INVALID_EVENT",
	-59: "CL_INVALID_OPERATION", -61: "CL_INVALID_BUFFER_SIZE",
	-63: "CL_INVALID_GLOBAL_WORK_SIZE",
}

// ErrorName returns the symbolic name of an OpenCL status, or the number when
// it is not one this package knows.
func ErrorName(status int32) string {
	if name, ok := errorNames[status]; ok {
		return name
	}
	return "unknown OpenCL error " + strconv.Itoa(int(status))
}
```

The two size-probe reads follow the OpenCL two-call idiom. `DeviceInfoUint64`
does **not** probe: every scalar device parameter this miner reads is
`cl_device_type`, which is `cl_bitfield` and therefore 8 bytes by the
specification.

```go
// DeviceInfoString returns a string-valued device parameter such as DeviceName.
func DeviceInfoString(device DeviceID, name DeviceInfo) (string, int32) {
	var size uint64
	if status := getDeviceInfo(device, name, 0, nil, &size); status != Success {
		return "", status
	}
	if size == 0 {
		return "", Success
	}

	buf := make([]byte, size)
	if status := getDeviceInfo(device, name, size, unsafe.Pointer(&buf[0]), nil); status != Success {
		return "", status
	}
	return string(bytes.TrimRight(buf, "\x00")), Success
}

// DeviceInfoUint64 returns a scalar device parameter.  Every one this miner
// reads is a cl_bitfield, which the specification fixes at 8 bytes.
func DeviceInfoUint64(device DeviceID, name DeviceInfo) (uint64, int32) {
	var value uint64
	status := getDeviceInfo(device, name, 8, unsafe.Pointer(&value), nil)
	return value, status
}
```

`CreateProgramWithSource` is the one place a Go pointer to a Go pointer crosses
the boundary, because the C parameter is `const char **`. OpenCL copies the
source into the program object and does not retain the pointer, so it only has
to survive the call:

```go
// CreateProgramWithSource creates a program from one source string, which is
// all this miner needs.
func CreateProgramWithSource(ctx Context, source string, errcode *int32) Program {
	src := []byte(source)
	if len(src) == 0 {
		*errcode = InvalidValue
		return 0
	}

	ptr := &src[0]
	length := uint64(len(src))
	program := createProgramWithSource(ctx, 1, &ptr, &length, errcode)
	runtime.KeepAlive(src)
	return program
}
```

The three enqueue wrappers exist only to hide event parameters that are always
nil here; without them every call site carries three bare zeros.

`Load` is a single function — it is called once, from `loadConfig`, and the test
above calls it directly:

```go
// Load resolves the OpenCL library and binds the entry points this package
// calls.  When path is non-empty it is the only candidate tried, which is the
// escape hatch for installations the built-in list misses.
func Load(path string) error {
	candidates := libraryCandidates
	if path != "" {
		candidates = []string{path}
	}

	var handle uintptr
	attempts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		h, err := openLibrary(candidate)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		handle = h
		break
	}
	if handle == 0 {
		return fmt.Errorf("unable to load the OpenCL library.  Tried:\n\t%s\n"+
			"Install your GPU vendor's OpenCL driver, or pass --opencl-lib with "+
			"the full path to it", strings.Join(attempts, "\n\t"))
	}

	return bind(handle)
}

// bind resolves every entry point.  purego panics when a symbol is missing,
// which for a library that loaded but is the wrong one would surface as a
// crash; convert it into an error naming the symbol.
func bind(handle uintptr) (err error) {
	var current string
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the OpenCL library does not provide %s: %v", current, r)
		}
	}()

	reg := func(name string, fptr any) {
		current = name
		purego.RegisterLibFunc(fptr, handle, name)
	}

	reg("clGetPlatformIDs", &GetPlatformIDs)
	reg("clGetDeviceIDs", &GetDeviceIDs)
	reg("clGetDeviceInfo", &getDeviceInfo)
	reg("clCreateContext", &CreateContext)
	reg("clCreateCommandQueue", &CreateCommandQueue)
	reg("clCreateBuffer", &CreateBuffer)
	reg("clCreateProgramWithSource", &createProgramWithSource)
	reg("clBuildProgram", &BuildProgram)
	reg("clGetProgramBuildInfo", &getProgramBuildInfo)
	reg("clCreateKernel", &CreateKernel)
	reg("clSetKernelArg", &SetKernelArg)
	reg("clEnqueueNDRangeKernel", &enqueueNDRangeKernel)
	reg("clEnqueueReadBuffer", &enqueueReadBuffer)
	reg("clEnqueueWriteBuffer", &enqueueWriteBuffer)
	reg("clReleaseMemObject", &ReleaseMemObject)
	reg("clReleaseKernel", &ReleaseKernel)
	reg("clReleaseProgram", &ReleaseProgram)
	reg("clReleaseCommandQueue", &ReleaseCommandQueue)
	reg("clReleaseContext", &ReleaseContext)

	return nil
}
```

- [ ] **Step 6: Write the two loaders**

`cl/loader_unix.go`:

```go
// Copyright (c) 2026 The Decred developers.

//go:build !windows

package cl

import "github.com/ebitengine/purego"

// libraryCandidates covers both Unix platforms in one list: a Linux soname
// simply fails to load on macOS and the loop moves on.
//
// Bare sonames are correct here.  dlopen does not search the working directory,
// so the library-planting vector that shapes the Windows loader does not exist
// on Unix, and absolute paths would only add per-distribution failure modes.
var libraryCandidates = []string{
	"libOpenCL.so.1",
	"libOpenCL.so",
	"/System/Library/Frameworks/OpenCL.framework/OpenCL",
}

func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}
```

`cl/loader_windows.go`:

```go
// Copyright (c) 2026 The Decred developers.

//go:build windows

package cl

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

var libraryCandidates = []string{"OpenCL.dll"}

const (
	loadLibrarySearchSystem32 = 0x00000800
	loadWithAlteredSearchPath = 0x00000008
)

// openLibrary loads a DLL while keeping the application directory out of the
// search.  LoadLibraryW("OpenCL.dll") searches the application directory first,
// and this miner ships as an archive users extract into a download directory,
// so a co-extracted OpenCL.dll would otherwise be loaded into the process that
// holds the node's RPC credentials.
func openLibrary(path string) (uintptr, error) {
	flags := uint32(loadLibrarySearchSystem32)
	if filepath.IsAbs(path) {
		// An absolute path can only have come from --opencl-lib, which is the
		// user's own explicit choice; restricting the search to System32 would
		// make it impossible to honour.
		flags = loadWithAlteredSearchPath
	}

	handle, err := windows.LoadLibraryEx(path, 0, flags)
	if err != nil {
		return 0, err
	}
	return uintptr(handle), nil
}
```

- [ ] **Step 7: Update the call sites**

In `config.go` add the escape hatch to the `config` struct, next to the other
config and log options:

```go
	OpenCLLib string `long:"opencl-lib" description:"Full path to the OpenCL library to load, for installations the built-in search does not cover"`
```

Then **move** the `if preCfg.ListDevices` block so it runs after the command
line has been re-parsed (after `remainingArgs, err := parser.Parse()`), and load
the library immediately before it:

```go
	// Resolve the OpenCL library before anything tries to use it, so a machine
	// with no driver installed gets a message naming the problem instead of a
	// failure inside the first CL call.
	if err := cl.Load(cfg.OpenCLLib); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, nil, err
	}

	if cfg.ListDevices {
		ListDevices()
		os.Exit(0)
	}
```

Moving it is what lets `--opencl-lib` work from the config file as well as the
command line. Add the `cl` import. `-V` keeps its early exit in the pre-parse
block, so `--version` still works with no driver installed.

In `device_opencl.go` the package-level types change from the C spelling to Go
types, and `getDeviceInfo`/`appendBitfield` are replaced by two helpers:

```go
const (
	outputBufferSize = uint64(64)
	localWorksize    = 64
	uint32Size       = uint64(unsafe.Sizeof(uint32(0)))
)

var zeroSlice = []uint32{0}

func clError(status int32, f string) error {
	return fmt.Errorf("%s returned error %s (%d)", f, cl.ErrorName(status), status)
}

// deviceName returns a device's CL_DEVICE_NAME.
func deviceName(id cl.DeviceID) string {
	name, status := cl.DeviceInfoString(id, cl.DeviceName)
	if status != cl.Success {
		return fmt.Sprintf("<unknown: %v>", clError(status, "clGetDeviceInfo"))
	}
	return name
}

// deviceTypeString renders a device's CL_DEVICE_TYPE as "CPU", "GPU" or both.
func deviceTypeString(id cl.DeviceID) string {
	value, status := cl.DeviceInfoUint64(id, cl.DeviceTypeParam)
	if status != cl.Success {
		return fmt.Sprintf("<unknown: %v>", clError(status, "clGetDeviceInfo"))
	}

	var s string
	if value&uint64(cl.DeviceTypeCPU) != 0 {
		s += DeviceTypeCPU
	}
	if value&uint64(cl.DeviceTypeGPU) != 0 {
		s += DeviceTypeGPU
	}
	return s
}
```

The two enumeration helpers show the shape of every remaining change — a nil
slice is how OpenCL's count-then-fetch idiom spells "just tell me the count":

```go
func getCLPlatforms() ([]cl.PlatformID, error) {
	var numPlatforms uint32
	if status := cl.GetPlatformIDs(0, nil, &numPlatforms); status != cl.Success {
		return nil, clError(status, "clGetPlatformIDs")
	}

	platforms := make([]cl.PlatformID, numPlatforms)
	if status := cl.GetPlatformIDs(numPlatforms, platforms, nil); status != cl.Success {
		return nil, clError(status, "clGetPlatformIDs")
	}
	return platforms, nil
}
```

`getCLDevices` follows the same shape with `cl.GetDeviceIDs(platform,
cl.DeviceTypeAll, ...)`, keeping its existing tolerance of
`cl.DeviceNotFound`.

The setup sequence in `NewDevice`:

```go
	d.context = cl.CreateContext(nil, 1, []cl.DeviceID{deviceID}, 0, 0, &status)
	...
	d.queue = cl.CreateCommandQueue(d.context, deviceID, 0, &status)
	...
	d.outputBuffer = cl.CreateBuffer(d.context, cl.MemReadWrite,
		uint32Size*outputBufferSize, nil, &status)
	...
	d.program = cl.CreateProgramWithSource(d.context, kernelSource, &status)
	if status != cl.Success {
		return nil, clError(status, "clCreateProgramWithSource")
	}

	options := fmt.Sprintf("-D WORKSIZE=%d", localWorksize)
	status = cl.BuildProgram(d.program, 1, []cl.DeviceID{deviceID}, options, 0, 0)
	if status != cl.Success {
		err := clError(status, "clBuildProgram")
		if log, s := cl.ProgramBuildLog(d.program, deviceID); s == cl.Success {
			minrLog.Errorf("Kernel build log:\n%s", log)
		}
		return nil, err
	}

	d.kernel = cl.CreateKernel(d.program, "search", &status)
```

And in both `device_opencl.go` and `calibrate.go` the hot loop:

```go
	status = cl.EnqueueWriteBuffer(d.queue, d.outputBuffer, false, 0, uint32Size,
		unsafe.Pointer(&zeroSlice[0]))
	...
	status = cl.EnqueueNDRangeKernel(d.queue, d.kernel, uint64(d.workSize), localWorksize)
	...
	status = cl.EnqueueReadBuffer(d.queue, d.outputBuffer, true, 0,
		uint32Size*outputBufferSize, unsafe.Pointer(&outputData[0]))
	if status != cl.Success {
		return clError(status, "clEnqueueReadBuffer")
	}
```

Note that both files currently **discard** the status of the read and then test
the stale `status` from the previous call — `device_opencl.go` line 687 and
`calibrate.go` line 83. Assign it properly, as above. It is the one blocking
call in the loop, and swallowing its error is how a dead queue becomes a silent
zero-hashrate run.

The remaining edits are mechanical: `cl.CL_int` becomes `int32`, `cl.CL_uint(i+1)`
becomes `uint32(i+1)`, `cl.CL_size_t(...)` becomes `uint64(...)`, and
`cl.CL_SUCCESS` becomes `cl.Success`, throughout both files. Let the compiler
find them.

- [ ] **Step 8: Build everywhere, then measure**

```sh
go mod tidy
go build ./...
go test ./...
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o /dev/null .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o /dev/null .
grep -rn '#cgo' --include='*.go' . ; echo "(no cgo directives above)"
```

Expected: every build succeeds and the grep is empty. Cross-compiling a Windows
binary from a laptop with no SDK is the payoff that decides task 7's CI shape.

Then the parity run, plus the multi-device check that stands in for the six-card
rig the spec says we do not have (§3.2, second risk): this machine exposes three
OpenCL devices, so running all of them at once is a real test of whether
purego's trampolines pin OS threads under the blocking read.

```sh
go build -o /tmp/monvark-t4 .
# Parity, single device.
( /tmp/monvark-t4 -B -D 2 & P=$!; sleep 330; kill $P ) 2>&1 | grep "DEV #2" | tail -3
# All devices at once; none should be starved.
( /tmp/monvark-t4 -B & P=$!; sleep 330; kill $P ) 2>&1 | grep "DEV #" | tail -6
```

Expected: `DEV #2` within 10% of the task 1 baseline, and in the all-device run
every device reporting a plausible non-zero rate rather than one holding the
others at zero.

- [ ] **Step 9: If purego does not hold**

Only if step 8 fails in a way that traces to purego rather than to a signature
mistake. Do not abandon the design on a first failure — a wrong argument width
in a bound signature looks exactly like a purego bug.

The fallback keeps the types, constants, the six real functions and every
call-site change verbatim, and replaces only the bound variables and
`openLibrary` with a cgo translation unit that `dlopen`s the library at run
time: the same 19 entry points, resolved the same way, with cgo trampolines.
Two of the four payoffs survive — no SDK on the build machine, and a binary that
starts without OpenCL in order to report the problem. What is lost is
`CGO_ENABLED=0`, so task 7's CI needs a three-runner matrix instead of one
cross-compiling job. Say so in the commit message.

- [ ] **Step 10: Commit**

```sh
git add -A
git commit -m "cl: Load OpenCL at run time instead of linking it.

The 4195-line cgo binding is replaced by the 19 entry points this miner
actually calls, resolved through purego at first use.  Four #cgo LDFLAGS
directives go with it, so the build no longer needs a vendor SDK and the
executable starts on a machine with no OpenCL installed, where it can report
that as an error rather than failing to launch.

purego passes slices as pointers and null-terminates strings itself, so
thirteen of the nineteen need no wrapper: the function variable is the API.
The package is named in idiomatic Go, which removes the last reason for the
revive var-naming exception.

Library search is hardened where it needs to be: dlopen does not search the
working directory, but LoadLibraryW searches the application directory first,
so Windows uses LOAD_LIBRARY_SEARCH_SYSTEM32 and a co-extracted OpenCL.dll is
not loaded into the process.  --opencl-lib is the escape hatch.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BFrJkoVTnN2SucvajpiSsX"
```

---

## Task 5: Prove the GPU and the host agree on the hash

Spec §4, "Host/GPU agreement, using the oracle that already exists". This is the
check that covers the **compiled** kernel end to end, which the host-only test of
task 2 cannot: a kernel that builds differently on a particular driver.

**The oracle already exists and needs no kernel edit.** `blake3.cl` takes no
target — its arguments are the output buffer, the midstate `cv0..cv7` and the
message words. It filters on `if (v7 ^ v15) return;` (line 154), a fixed
comparison against zero rather than against the network target, which the host
applies later. **So every candidate the GPU emits has a hash whose final 32-bit
word is zero**, and `foundCandidate` already recomputes that hash host-side with
`blake3.FinalBlock(d.midstate, data[128:180])`.

The invariant is worth enforcing in production, not only in a test: a candidate
that fails it means a miscompiled kernel, a wrong midstate or a byte-order slip,
which is exactly the hardware-error class `foundCandidate` already reports. So
the check goes into `foundCandidate` and the test asserts the counter stays at
zero.

At the baseline 2.08 GH/s one candidate arrives every `2^32 / 2.08e9` ~ 2.1 s,
so this is a fast test rather than a soak.

**Files:**
- Modify: `device.go`
- Create: `device_test.go`
- Test: `device_test.go`

**Interfaces:**
- Consumes: `cl.Load` and `newMinerDevs` (task 4), `Device.Run`,
  `Device.SetWork`, `Device.Release`
- Produces:
  - `Device.hashMismatches uint64` — count of candidates whose host-recomputed
    hash did not end in a zero word. Read it under the device lock, the same way
    `allDiffOneShares` is.

- [ ] **Step 1: Write the failing test**

Create `device_test.go`:

```go
// Copyright (c) 2026 The Decred developers.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/decred/gominer/cl"
	"github.com/decred/gominer/work"
)

// TestDeviceHashAgreesWithHost runs the compiled kernel on every OpenCL device
// present and checks that each candidate they emit hashes, on the host, to a
// value whose final 32-bit word is zero.
//
// The kernel applies no target of its own -- it returns unless (v7 ^ v15) is
// zero -- so that is an invariant of every candidate rather than a property of
// this particular work item.  A miscompiled kernel, a wrong midstate or a
// byte-order slip breaks it immediately.
//
// Every device is exercised rather than a chosen one: the CPU and integrated
// devices run the same kernel through the same driver stack, so including them
// costs a few seconds of calibration and tests more hardware.
//
// The test skips where no OpenCL library or device is available, so it is a
// no-op on a CI runner and real on a developer machine.
func TestDeviceHashAgreesWithHost(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping GPU test in short mode")
	}
	if err := cl.Load(""); err != nil {
		t.Skipf("no OpenCL library available: %v", err)
	}

	// Benchmark mode gives each device a zero-valued work item and stops
	// foundCandidate from trying to submit anything.
	cfg = &config{Benchmark: true, AutocalibrateInts: []int{500}}

	devices, err := newMinerDevs(make(chan []byte, 10))
	if err != nil {
		t.Skipf("could not open the OpenCL devices: %v", err)
	}
	if len(devices) == 0 {
		t.Skip("no OpenCL devices")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, len(devices))
	for _, d := range devices {
		device := d
		go func() {
			device.Run(ctx)
			done <- struct{}{}
		}()
		device.SetWork(ctx, &work.Work{})
	}

	// At the reference rig's ~2.1 s per candidate this is roughly seven from a
	// fast device; slower ones contribute fewer, and the assertion below only
	// needs the total to be non-zero.
	time.Sleep(15 * time.Second)
	cancel()
	for range devices {
		<-done
	}

	var candidates, mismatches uint64
	for _, d := range devices {
		d.Lock()
		candidates += d.allDiffOneShares
		mismatches += d.hashMismatches
		d.Unlock()
		d.Release()
	}

	if candidates == 0 {
		t.Fatalf("%d device(s) produced no candidates in 15s", len(devices))
	}
	if mismatches != 0 {
		t.Fatalf("%d of %d candidates hashed differently on the host than on "+
			"the GPU", mismatches, candidates)
	}
	t.Logf("%d device(s), %d candidates, all agreeing with the host",
		len(devices), candidates)
}
```

- [ ] **Step 2: Run the test to verify it fails**

```sh
go test -run TestDeviceHashAgreesWithHost ./
```

Expected: FAIL to compile — `d.hashMismatches undefined`.

- [ ] **Step 3: Enforce the invariant in `foundCandidate`**

Add the counter to the `Device` struct in `device_opencl.go`, beside the other
share counters:

```go
	started          uint32
	allDiffOneShares uint64
	validShares      uint64
	invalidShares    uint64

	// hashMismatches counts candidates whose host-recomputed hash did not end
	// in a zero word, which means the host and the GPU disagree.
	hashMismatches uint64
```

In `device.go`, insert the check into `foundCandidate` immediately after the
hash is computed and **before** the existing `PowLimit` check:

```go
	hash := chainhash.Hash(blake3.FinalBlock(d.midstate, data[128:180]))

	// The kernel applies no target of its own: it discards everything whose
	// final 32-bit hash word is non-zero (blake3.cl, "if (v7 ^ v15) return;")
	// and leaves the target comparison to the host.  So every candidate that
	// reaches here must hash to a value ending in a zero word.  One that does
	// not means the host and the GPU disagree about the hash -- a miscompiled
	// kernel, a wrong midstate, or a byte-order slip -- rather than an unlucky
	// nonce.
	if binary.LittleEndian.Uint32(hash[28:]) != 0 {
		minrLog.Errorf("DEV #%d: GPU and host disagree on the hash of a "+
			"candidate: %v does not end in a zero word", d.index, hash)
		d.hashMismatches++
		d.invalidShares++
		return
	}
```

`encoding/binary` is already imported by `device.go`.

- [ ] **Step 4: Run the test to verify it passes**

```sh
go test -run TestDeviceHashAgreesWithHost -v ./
```

Expected: PASS, with a log line naming the device and the candidate count. On a
machine with no GPU or no OpenCL library it must SKIP, not fail — check that too
if you can:

```sh
go test -short -run TestDeviceHashAgreesWithHost -v ./
```

Expected: SKIP.

- [ ] **Step 5: Commit**

```sh
git add device.go device_opencl.go device_test.go
git commit -m "device: Check that the GPU and the host agree on hashes.

The kernel takes no target and discards everything whose final 32-bit hash
word is non-zero, so that is an invariant of every candidate it emits rather
than a property of one work item.  Checking it host-side turns a miscompiled
kernel, a wrong midstate or a byte-order slip into a named error instead of a
silent stream of rejected shares, and gives a test that exercises the compiled
kernel end to end.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BFrJkoVTnN2SucvajpiSsX"
```

---

## Task 6: Rename to `monvark`

Spec §5 step 5, §8. The window is now: after tasks 1-4 the import surface is at
its smallest — three of the seven packages the old `CLAUDE.md` called
"load-bearing" are deleted and a fourth has been rewritten — and no public
artifact or per-user config directory exists yet, so nothing needs migrating.

**Hard deadline, from §8.2:** the name must be final **before the first
`git tag`**. `proxy.golang.org` caches a published module version permanently and
does not allow deletion.

**Owner actions, not automatable — do these first:**

1. Create the empty repository `github.com/edshav/monvark` (public). For Go the
   repository URL *is* the module path, so creating the repository is claiming
   the name. The name was checked and is clean: zero repositories on GitHub,
   zero modules on `pkg.go.dev` (§8.6).
2. Decide what happens to `origin`, which currently points at the private
   `edshav/monetarium-gominer`, and to the pinned `master` branch. §8.3 notes
   `master` has lost its purpose: it was kept so `git diff master..<branch>`
   showed the Monetarium delta, but upstream `decred/gominer` has not moved
   since January 2024 and its HEAD **is** the fork point `68791b0`. **Do not
   change remotes as part of this task** — flag it and let the owner decide.

**Files:**
- Modify: `go.mod`, every `.go` file importing an internal package, `config.go`,
  `main.go`, `.gitignore`
- Rename: `sample-gominer.conf` → `sample-monvark.conf` (contents are rewritten
  in task 8)
- Test: existing tests, re-run

**Interfaces:**
- Consumes: everything from tasks 1-5
- Produces: module path `github.com/edshav/monvark`; packages import as
  `github.com/edshav/monvark/{work,util,cl,blake3}`; `minerHomeDir` resolves to
  `~/.monvark/`

- [ ] **Step 1: Rewrite the module path and the imports**

```sh
go mod edit -module github.com/edshav/monvark

# macOS sed; on GNU sed drop the '' after -i.
grep -rl 'github.com/decred/gominer' --include='*.go' . \
  | xargs sed -i '' 's|github.com/decred/gominer|github.com/edshav/monvark|g'

go mod tidy
go build ./...
```

Expected: builds. The spec's count is 19 import lines across four packages
(`work` 8, `util` 7, `cl` 3, `blake3` 1); the five in `stratum`, `nvml` and
`adl` went with those packages in task 1.

- [ ] **Step 2: Move the application directory and the file names**

In `config.go`:

```go
const (
	defaultConfigFilename = "monvark.conf"
	defaultLogLevel       = "info"
	defaultLogDirname     = "logs"
	defaultLogFilename    = "monvark.log"
)

var (
	minerHomeDir = dcrutil.AppDataDir("monvark", false)
	nodeHomeDir  = dcrutil.AppDataDir("monetarium", false)
	...
)
```

`nodeHomeDir` stays `monetarium` — it points at the node's own directory, which
this rename does not touch. Stage 2 replaces its use anyway.

In `main.go` rename `gominerMain` to `monvarkMain` and update the call in
`main`.

In `.gitignore` change the bare `gominer` line to `monvark`.

```sh
git mv sample-gominer.conf sample-monvark.conf
```

- [ ] **Step 3: Check nothing still refers to the old name in code**

```sh
grep -rn "gominer" --include='*.go' . ; echo "(nothing above)"
grep -rn "gominer" .gitignore go.mod ; echo "(nothing above)"
```

Expected: both empty. `README.md`, `CLAUDE.md` and `sample-monvark.conf` still
mention it — task 8 rewrites them.

Internal identifiers keep their `dcr*` heritage on purpose: deeper de-Decred
renaming is out of scope (§6).

- [ ] **Step 4: Verify the build, the tests and the new home directory**

```sh
go build -o /tmp/monvark-t6 . && go test ./...
/tmp/monvark-t6 -l
ls -la ~/.monvark/
```

Expected: the build and tests pass, the device list prints, and `~/.monvark/`
exists — `loadConfig` creates it with mode 0700. On macOS the real path is
`~/Library/Application Support/Monvark/`; `dcrutil.AppDataDir` handles the
platform difference.

- [ ] **Step 5: Verify parity under the new name**

```sh
( /tmp/monvark-t6 -B -D 2 & P=$!; sleep 330; kill $P ) 2>&1 | grep "DEV #2" | tail -3
```

Expected: within 10% of the task 1 baseline. A rename should not move it; the
run is here because this is the last task that touches the mining path in this
stage.

- [ ] **Step 6: Commit**

```sh
git add -A
git commit -m "main: Rename gominer to monvark.

The module path, the binary and the application directory become monvark; the
mon prefix groups the miner with mond and monctl.  Internal package names and
identifiers keep their Decred heritage, which is deliberate and out of scope
here.

The rename lands now because the import surface is at its smallest -- three of
the packages that carried it are deleted and a fourth was rewritten -- and
because a module path is permanent once proxy.golang.org has seen a tag.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BFrJkoVTnN2SucvajpiSsX"
```

---

## Task 7: Green CI producing artifacts for every shipped platform

Spec §5 step 6, §7. **This task starts from broken, not from green.** Two
independent reasons, both verified:

1. `.github/workflows/go.yml` builds against Go 1.20 and 1.21 while `go.mod`
   requires 1.23, and `apt-get install`s the CUDA toolkit on a single Ubuntu
   runner for a backend that no longer exists.
2. `.golangci.yml` is in golangci-lint **v1** format. Run against the current
   v2 release it fails outright with
   `can't load config: unsupported version of the configuration: ""`, so
   `run_tests.sh` — the CI entrypoint — cannot even start the linters.

Because task 4 removed cgo, one Linux runner can now cross-compile every shipped
platform. There is no three-runner matrix unless task 4's fallback was taken.

Two shipped artifacts, not six. `linux/amd64` and `windows/amd64` are what spec
§1 asks for — AMD and nVidia rigs, Windows required. arm64 has no desktop GPU
OpenCL story worth shipping for, and spec §6 has not decided whether macOS is a
shipped artifact at all, on a platform where Apple deprecated OpenCL. macOS is
still compiled in CI, because it is the development platform and a Linux runner
would not otherwise build `loader_unix.go`'s macOS path. Add a row when someone
asks for one.

**Files:**
- Modify: `.golangci.yml`, `.github/workflows/go.yml`, `run_tests.sh`,
  `util/util.go`, `config.go`, `device_opencl.go`
- Test: the CI run itself

**Interfaces:**
- Consumes: the cgo-free build from task 4, the `monvark` name from task 6
- Produces: CI artifacts `monvark-linux-amd64` and `monvark-windows-amd64.exe`

- [ ] **Step 1: Migrate the linter configuration to v2**

The v2 schema needs an explicit `version`, renames `deadline` to `timeout` and
`disable-all` to `default: none`, moves linter settings under `linters.settings`,
and moves the formatters out into their own section. Two entries from the old
list no longer exist: `gosimple` and `typecheck` are folded into `staticcheck`,
and `exportloopref` is obsolete since Go 1.22 gave loop variables per-iteration
scope. Replace `.golangci.yml` with:

```yaml
version: "2"

run:
  timeout: 10m

linters:
  default: none
  enable:
    - asciicheck
    - bidichk
    - bodyclose
    - containedctx
    - dupword
    - durationcheck
    - errorlint
    - godot
    - grouper
    - ineffassign
    - makezero
    - misspell
    - nosprintfhostport
    - prealloc
    - predeclared
    - reassign
    - revive
    - rowserrcheck
    - staticcheck
    - tparallel
    - unconvert
    - unparam
    - unused

formatters:
  enable:
    - gofmt
    - goimports
```

Two things are gone from the old config. `build-tags: [opencl]`, because task 3
removed the tag. And the `revive` `var-naming` exception: it existed because
"the codebase uses underscores", and every underscore identifier in the
repository lived in `cl/`, `nvml/` and `adl/`, which tasks 1 and 4 delete — task
4's replacement is named in idiomatic Go.

If `golangci-lint run` still reports `var-naming` in surviving code, fix the
names rather than restoring the exception; restore it only if what it flags
turns out to be something that genuinely cannot be renamed.

- [ ] **Step 2: Fix the findings that survive the deletions**

Running the config above against the tree as it was before this plan reported 13
issues. Eight of them are inside files tasks 1 and 4 delete. These five are in
code that survives, and `staticcheck` in v2 subsumes the stylecheck and quickfix
checks that `gosimple` did not run, which is why three of them are new:

```
util/util.go:73          predeclared: variable max has same name as a predeclared identifier
device_opencl.go:484     QF1007: could merge conditional assignment into variable declaration
config.go:179,194,205    ST1005: error strings should not be capitalized
```

In `util/util.go` rename the local `max` — it shadows the builtin added in
Go 1.21:

```go
	// (in DiffToTarget) rename max to maxTarget throughout the function
	maxTarget := new(big.Int).Set(powLimit)
```

In `device_opencl.go` collapse the conditional assignment in `NewDevice`:

```go
	// The intensity or worksize must be set by the user.
	userSetWorkSize := len(cfg.IntensityInts) > 0 || len(cfg.WorkSizeInts) > 0
```

In `config.go` lower-case the three error strings in `parseAndSetDebugLevels`:

```go
		str := "the specified debug level [%v] is invalid"
		...
		str := "the specified debug level contains an invalid subsystem/level pair [%v]"
		...
		str := "the specified subsystem [%v] is invalid -- supported subsystems %v"
```

Then confirm:

```sh
golangci-lint run
```

Expected: no issues. If your local golangci-lint is older than v2, install v2
first — the config above will not load under v1 either, which is the point.

- [ ] **Step 3: Simplify `run_tests.sh`**

```sh
#!/usr/bin/env bash
#
# Copyright (c) 2020-2026 The Decred developers
# Use of this source code is governed by an ISC
# license that can be found in the LICENSE file.
#
# Usage:
#   ./run_tests.sh

set -e

go version

# Run tests.  The GPU tests skip when no OpenCL device is present, so this is
# also the CI entrypoint.
go test -v ./...

# Run linters.
golangci-lint run

echo "-----------------------------"
echo "Tests completed successfully!"
```

The only change from today is dropping `-tags opencl`.

- [ ] **Step 4: Rewrite the workflow**

Replace `.github/workflows/go.yml`:

```yaml
name: Build and Test
on: [push, pull_request]
permissions:
  contents: read

jobs:
  test:
    name: Test and lint
    runs-on: ubuntu-24.04
    strategy:
      matrix:
        go: ['1.23', '1.24']
    steps:
      - name: Check out source
        uses: actions/checkout@v4
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go }}
      - name: Install linters
        run: |
          curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
            | sh -s -- -b "$(go env GOPATH)/bin" v2.12.2
      - name: Test and lint
        run: ./run_tests.sh

  build:
    name: Build every shipped platform
    runs-on: ubuntu-24.04
    steps:
      - name: Check out source
        uses: actions/checkout@v4
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - name: Cross-compile
        env:
          CGO_ENABLED: '0'
        run: |
          set -eu
          mkdir -p dist
          GOOS=linux   GOARCH=amd64 go build -trimpath -o dist/monvark-linux-amd64 .
          GOOS=windows GOARCH=amd64 go build -trimpath -o dist/monvark-windows-amd64.exe .
          # macOS is a development target, not a shipped artifact (spec §6), so
          # it is only compiled here to keep loader_unix.go honest.
          GOOS=darwin  GOARCH=arm64 go build -trimpath -o /dev/null .
          ls -l dist
      - name: Upload artifacts
        uses: actions/upload-artifact@v4
        with:
          name: monvark-binaries
          path: dist/
```

No `apt-get` step: with cgo gone there is nothing to install, which is the
concrete payoff of task 4. The GPU tests skip on the runner because
`cl.Load("")` finds no library there, which is the behaviour task 5 was written
to have.

**Pin the actions before merging.** The repository's existing style pins to a
commit SHA with the tag in a trailing comment. Look each one up rather than
inventing it:

```sh
for a in actions/checkout actions/setup-go actions/upload-artifact; do
  gh api "repos/$a/git/ref/tags/v4" --jq "\"$a \" + .object.sha" 2>/dev/null
done
```

and use `actions/setup-go@v5`'s own SHA for that one.

- [ ] **Step 5: Verify locally what CI will do**

```sh
./run_tests.sh
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -o /tmp/dist/monvark-linux-amd64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o /tmp/dist/monvark-windows-amd64.exe .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -o /dev/null .
file /tmp/dist/*
```

Expected: tests and linters pass, both binaries build, and `file` reports a
PE32+ executable for Windows built from a machine with no Windows SDK.

- [ ] **Step 6: Verify the Windows loader on Windows**

The one thing cross-compilation cannot check is whether
`windows.LoadLibraryEx` actually resolves `OpenCL.dll` on a real machine. On a
Windows box with an AMD or nVidia driver installed:

```
monvark-windows-amd64.exe -l
monvark-windows-amd64.exe -B
```

Expected: the device list names the GPU, and the benchmark reports a plausible
hash rate. Also confirm the hardening does what it is for: copy an unrelated DLL
named `OpenCL.dll` next to the executable and re-run `-l`. It must still load
the system library and list devices, **not** the planted file.

If no Windows machine is available, say so in the commit message rather than
claiming the step passed — spec §6 leaves Windows code signing open as an owner
question, and this step's result feeds that conversation.

- [ ] **Step 7: Commit**

```sh
git add .golangci.yml .github/workflows/go.yml run_tests.sh util/util.go config.go device_opencl.go
git commit -m "build: Repair CI and cross-compile every shipped platform.

CI was red on two counts: the workflow pinned Go 1.20 and 1.21 against a
go.mod that requires 1.23 and installed a CUDA toolkit for a backend that no
longer exists, and .golangci.yml was in the v1 format, which current
golangci-lint refuses to load at all.

With cgo gone one Linux runner now cross-compiles Linux, Windows and macOS on
amd64 and arm64 with no SDK, so the workflow builds all six and uploads them.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BFrJkoVTnN2SucvajpiSsX"
```

---

## Task 8: Bring the documentation in line with the code

Spec §7. These files contradict the code today and will contradict it far more
after tasks 1-7. This is the last task of the stage because it describes what
the previous seven produced.

`README.md` and `sample-gominer.conf` still describe `dcrd`, `~/.dcrd` and port
9109 — stale since the Monetarium retarget, before this plan. Everything about
build tags, CUDA, ADL, pools, `--kernel`, `--temptarget` and `--experimental`
became false during this stage.

**Scope note:** this stage's README describes a miner that mines against a node
the user configured themselves, because that is what stage 1 produces. Do not
document the download-and-run flow — first run, node ownership and the archive
are stage 2, and documenting them now would be documenting something that does
not exist.

**Files:**
- Modify: `README.md`, `sample-monvark.conf`, `CLAUDE.md`
- Test: none — verify by following the README on a clean checkout

**Interfaces:**
- Consumes: the finished state of tasks 1-7
- Produces: nothing consumed by later tasks

- [ ] **Step 1: Rewrite `README.md`**

Cover, and nothing beyond:

- what it is — a BLAKE3 OpenCL GPU miner for Monetarium, solo only, AMD and
  nVidia;
- requirements — a GPU vendor's OpenCL driver, and Go 1.23 to build. **No SDK,
  no CUDA toolkit, no build tags.** `go build .` is the whole build;
- running the benchmark — `monvark -B`, and `monvark -l` to list devices;
- solo mining against a node the user runs, with the flags that actually exist:
  `--rpcuser`, `--rpcpass`, `--rpcserver`, `--rpccert`, plus `--testnet` /
  `--simnet`. State the ports: **9509** mainnet, **19509** testnet, **19956**
  simnet, and that the certificate lives under the `monetarium` application data
  directory;
- **the node must have `generate=0`** — `getwork` is refused while CPU mining is
  enabled, with "getwork polling is disallowed while CPU mining is enabled" —
  while `miningaddr` must stay, because the node needs it to build the coinbase;
- tuning — `--intensity`, `--worksize`, `--autocalibrate`, `--devices`, and the
  fact that `--intensity` sets the work size per kernel launch and does **not**
  throttle the device or reduce heat;
- `--opencl-lib` for installations where the library is somewhere the built-in
  search does not look;
- `--apilisten` for the read-only status JSON, default port 3333.

Delete every mention of `dcrd`, `~/.dcrd`, port 9109, CUDA, `go generate`, build
tags, stratum and pools, `--kernel`, `--temptarget` and `--experimental`. State
plainly that fan control and temperature reporting were removed and that
`nvidia-smi` and `rocm-smi` do that job properly.

- [ ] **Step 2: Rewrite `sample-monvark.conf`**

Mirror the README: every remaining flag, commented out, with the real defaults.
Remove the pool section, the CUDA section, `temptarget`, `experimental` and
`kernel` entirely. Correct the RPC section to the Monetarium ports and the
`monetarium` application data directory.

- [ ] **Step 3: Update `CLAUDE.md`**

Four sections are now false:

- **"Build: a GPU backend tag is mandatory"** — delete it. Replace with a short
  section saying `go build .` is the whole build, there are no build tags, and
  `CGO_ENABLED=0` cross-compilation works for every shipped platform;
- **the paragraph forbidding the rename** ("Do not 'finish' the rebrand unless
  asked", and the list of packages the `gominer` name is load-bearing across) —
  delete it. Three of those seven packages no longer exist and a fourth was
  rewritten. Replace with the current naming rule: module, binary and config
  directory are `monvark`; internal identifiers keep their `dcr*` heritage;
- **the git layout paragraph** — update it for whatever the owner decided in
  task 6 about `origin` and `master`. If nothing was decided, say so rather than
  describing a layout that is no longer true;
- **"Known stale spots"** — the README, sample conf and workflow entries are
  resolved by this task and task 7. Replace the section with what is genuinely
  still outstanding, which at the end of this stage is: stage 2 is not built,
  and `getblocktemplate` does not exist on the node so the spec's payee check
  needs redesigning.

Add a line pointing at `cl/`: the OpenCL binding is loaded at run time through
purego and covers exactly 19 entry points; adding a call means adding it to the
bound variables in `cl/cl.go` and to `bind` in the same file.

Also record the hashrate-measurement gotcha, which cost real time to discover:
the reported rate is a cumulative average since process start, so a benchmark
must run for at least five minutes before its number means anything.

- [ ] **Step 4: Verify by following the README**

On a clean clone, do exactly what the README says and nothing else:

```sh
git clone <repo> /tmp/monvark-readme-check && cd /tmp/monvark-readme-check
go build .
./monvark -l
./monvark -B -D <gpu index>
```

Expected: each command works as documented. If a step needs knowledge the README
does not give, the README is wrong — fix it, do not work around it.

- [ ] **Step 5: Commit**

```sh
git add README.md sample-monvark.conf CLAUDE.md
git commit -m "docs: Describe the miner that now exists.

README and the sample config still described dcrd, ~/.dcrd and port 9109, and
everything about build tags, CUDA, pools, --kernel and --temptarget became
false over this branch.  CLAUDE.md's build-tag section and its instruction not
to finish the rebrand no longer apply.

This documents solo mining against a node the user runs themselves, which is
what this stage produces; first run and the archive are stage 2.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BFrJkoVTnN2SucvajpiSsX"
```

---

## Definition of done for stage 1

All of these hold at the end of task 8:

- [ ] `go build ./...` succeeds with **no build tags**
- [ ] `CGO_ENABLED=0 go build ./...` succeeds, and cross-compiles for
      linux/amd64, windows/amd64 and darwin/arm64 from one machine
- [ ] no `#cgo` directive referencing a GPU driver remains in the tree
- [ ] `./run_tests.sh` passes: tests and golangci-lint v2
- [ ] the binary runs from a directory unrelated to its source
- [ ] benchmark hashrate on the reference GPU is within 10% of the task 1
      baseline
- [ ] all devices run concurrently without starving each other
- [ ] `blake3` agrees with an independent implementation; the compiled kernel
      agrees with the host
- [ ] module, binary and config directory are `monvark`; `~/.monvark/` is used
- [ ] CI is green and uploads the linux/amd64 and windows/amd64 binaries
- [ ] README, sample config and `CLAUDE.md` describe this miner

**Not done, and deliberately so** — these are stage 2: first run and the payout
address question, generating and supervising `mond`, the sync gate, the payee
assertion, work expiry, the archive and the release. Until stage 2 lands, a user
still has to configure a node by hand, and **nothing in the system verifies who
the coinbase pays.**

---

## Stage 2 preview — and what stage 1 must not box in

Here so that an executor working one task at a time knows where this is going.
**Nothing in this section is built in stage 1.** The authority for it is the
spec — §3.7, §4 "Payee verification" and §5 steps 7-8 — not this summary; it is
deliberately a pointer rather than a copy, so there is one source of truth.

**The shape of stage 2**, one line per planned task:

1. **First run.** Ask for the payout address once. Validate it with
   `stdaddr.DecodeAddress` against the active `chainParams` **before writing
   anything**, so a typo or a testnet address on mainnet is reported in the
   miner's own words. Write `~/.monvark/` config `0600`; refuse to start if it
   is group- or world-writable. A second run does not ask again and does not
   rewrite the file; a conflicting one is an error naming the file, never a
   silent overwrite.
2. **Node ownership.** Generate `mond`'s configuration and start it as a child
   process on a loopback port monvark chooses, with an `--rpccert` under
   `~/.monvark/` — so there is no well-known port to hijack and no shared
   certificate to replace. `generate=0` is mandatory: a node with CPU mining
   enabled refuses `getwork`.
3. **Sync gate.** Gate on `!InitialBlockDownload` from `getblockchaininfo` and
   render `VerificationProgress` as the progress bar. **Not** `blocks >= headers`
   — on a clean machine both are 0 before any header arrives, which reads as
   "synced" and mines at height 1.
4. **Payee verification.** Needs redesign before it can be built — see
   Deviations, item 4. This is the only check in the whole system that covers
   where the money goes rather than whether the hash is right.
5. **Work expiry.** If no pushed work has arrived within a bound, re-issue
   `GetWork` — the guarded path — and surface the condition. The push path in
   the node carries neither of the guards `handleGetWork` has, and
   `updateCurrentWork` keeps the current work forever, so a dropped websocket
   grinds a dead header at full reported hash rate.
6. **Packaging.** One archive of `monvark` and `mond`, published through GitHub
   Releases with its SHA256 in the notes.

**Forward constraints.** These are the stage 1 outcomes stage 2 has to live
with. Read the right-hand column before changing anything in the left.

| Stage 1 leaves it this way | Why stage 2 cares |
|---|---|
| `--rpcserver`, `--rpccert`, `--rpcuser`, `--rpcpass` are kept and documented in task 8 | §3.7 removes the attach-to-an-external-node mode outright, because a `getwork` template pays the `miningaddr` of whichever node served it. These flags become internal or disappear, and task 8's README section about them is rewritten. Do not build anything else on top of them. |
| `nodeHomeDir` still resolves to the node's own `~/.monetarium/` | Stage 2 replaces its use entirely: monvark picks the port and the certificate path under `~/.monvark/`. |
| `main.go` runs `loadConfig` → `NewMiner` → `Run` | Stage 2 inserts first run, node start and the sync gate between `loadConfig` and `NewMiner`. Keep `NewMiner` free of anything that assumes a node is already reachable, so that insertion stays a change to `main.go` alone. |
| `work.Work.TimeReceived` is untouched and still unread | Stage 2's expiry check needs it. Nothing in the repository consults it today, which is exactly the gap. |
| `monitor.go`'s status JSON was trimmed | It grows again: the payout address has to appear there and in the status output. `CoinbaseMaturity` is 256, so at the measured block interval the user cannot otherwise confirm they were paid for the better part of a day. |
| Benchmark mode (`-B`) stays a path that needs no node | It is the only way to exercise the mining loop without one — every parity and GPU check in this plan depends on it — and stage 2's supervisor must not start for it. |
| The kernel is embedded and `--kernel` is gone | Stage 2 ships a bare executable users unpack anywhere. A kernel resolved from disk would fail on every machine but the build one. |

**Two owner questions from spec §6 block the stage 2 release, not stage 1:**
whether a Windows code-signing certificate is budgeted (unsigned miner binaries
are quarantined by Defender and SmartScreen largely by category), and whether
macOS is a shipped artifact or only a development target. Task 7 step 6 produces
the input for the first one.

---

## Self-review

Checked after writing, against the spec.

**Spec coverage.** §2.4 (OpenCL only) → task 1. §3.1 (deletion) → tasks 1, 4.
§3.2 (runtime GPU access, hardened search, `--opencl-lib`, purego with a
fallback) → task 4. §3.3 (no build tags, no `Device` interface, the file rename
inside the same step) → task 3. §3.4 (fan control and temperature) → task 1.
§3.5 (self-contained binary, `go:embed`, `--kernel` deleted) → task 3. §3.6
(stratum deleted, nonce separation untouched) → task 1, and the `Nonce3Word`
constraint is in Global Constraints. §4 host side → task 2; §4 GPU/host
agreement → task 5; §4 `dlopen` error naming the library and the symbol →
task 4 steps 2 and 7; §4 regression metric → Global Constraints and every parity
step. §5 steps 1-6 → tasks 1-7. §7 → tasks 7 and 8. §8 → task 6.

**Deliberately not covered here:** §3.7 and §5 steps 7-8, which are stage 2 and
are listed under Scope and Definition of done. §4's "two devices receive
different `Nonce3Word` device ids" is stage-2-adjacent — the nonce code is
untouched in this stage, so there is nothing here that could break it, and the
spec itself notes the honest version of that test asserts only probabilistic
separation.

**Two spec items that cannot be met as written**, both recorded under Deviations
with the evidence: `getblocktemplate` does not exist on the node, and the work
order is reordered so the deletions precede the purego decision.

**Numbers in this plan that were measured rather than assumed:** the 2.08-2.09
GH/s baseline and the fact that a 45-second run reports 1.47; that
`blake3.FinalBlock` equals `lukechampine.com/blake3.Sum256` over the same 180
bytes; that purego passes a Go slice as its data pointer and null-terminates a
Go string itself, which is what removes thirteen of the nineteen wrappers; that
purego v0.10.2 declares
`go 1.18` while v0.11.0 declares `go 1.25.0`; that purego's Tier 1 covers all
six target platforms with no cgo requirement and that `purego.Dlopen` excludes
Windows; that the current `.golangci.yml` fails to load under golangci-lint v2
and which five findings survive the deletions; and that the node exposes no
`getblocktemplate` handler.

**This plan has been through one review pass.** What changed: the `cl` package
lost thirteen wrappers, eight scalar type aliases, a `sync.Once`, three files and
the Linux absolute-path candidates; `ErrorName` became a map; the blake3 fixed
vector, the GPU test's device-picking machinery and `baseline.txt` were deleted;
CI went from six artifacts to two. Net about 290 lines less shipped Go and 320
lines less plan. One item was only partly taken: the reviewer asked for all
~630 lines of pasted Go to come out of the plan, leaving the Interfaces block as
the contract. The Interfaces block does pin the contract, but the writing-plans
skill this document follows requires real code in code steps, and the bound
purego signatures are exactly where a silent width mistake produces memory
corruption rather than a compile error. What remains pasted is the code that
encodes a non-obvious decision; what was mechanical is now described and left to
the compiler to find.

**Open questions from spec §6 that this stage does not answer:** Windows code
signing (task 7 step 6 produces the input for that conversation) and whether
macOS is a shipped artifact or a development target. Neither blocks stage 1;
both block the stage 2 release.
