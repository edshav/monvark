# Design: Monetarium solo GPU miner

Date: 2026-09-06
Revised: 2026-09-06, after a five-lens reader test and a cross-repository survey.
Status: accepted for implementation
Repository: `gominer` (branch `monetarium-retarget`); renamed to `monvark`
at step 5 of the work order.

## 1. Goal

Turn `gominer` into a GPU miner suitable for public distribution: the user
downloads an archive, runs it, answers one question, and mines.

Requirements as stated by the owner:

| # | Requirement | Answer |
|---|---|---|
| 1 | Who runs it | Public miners, arbitrary hardware and OS |
| 2 | Vendors | AMD + nVidia |
| 3 | Mode | Solo only |
| 4 | Proof of work | Frozen permanently |
| 5 | Multi-GPU | Required |
| 6 | Windows | Required; may be deferred if expensive |

Two questions in §6 are still open and can only be answered by the owner.

## 2. Measurements the decisions rest on

Figures taken from mainnet on 2026-09-06 at height 31 023.

### 2.1 Network

```
networkhashps                  199 958 106 928   ≈ 200 GH/s   (single reading)
block interval (measured)      241.6 s           heights 30735 → 31023
                                                 1788666058 → 1788735634
block interval (target)        300 s             chaincfg TargetTimePerBlock
PoW reward                     32 VAR / block    getblocksubsidy 31023 5
mainnet chain size on disk     312 MB
```

**Provenance caveat.** `networkhashps` is a rolling estimate, not a constant.
A re-reading hours later returned 190 813 422 718 (−4.6 %), and a workspace
note from 2026-09-05 recorded ~140 GH/s. Everything derived in §2.2 is
therefore accurate to roughly ±10 %, which is ample for the decisions it
supports and not enough to quote to three digits elsewhere.

The measured interval is below target, meaning difficulty trails a rising
hashrate. Note that 241.6 s is the **optimistic** input for a
"you find blocks often enough" argument: relative to the 300 s target it
raises every blocks/day figure below by 24 % and shortens every gap by 20 %.
The conservative reading of §2.2 is to apply that discount mentally.

### 2.2 A single card's share of the network

| Card | Hashrate | Share | Blocks/day | Mean gap |
|---|---|---|---|---|
| RTX 4070 | 14.85 GH/s | 7.43 % | 26.6 | 0.9 h |
| Vega 56 | 7.00 GH/s | 3.50 % | 12.5 | 1.9 h |
| RX 580 | 3.68 GH/s | 1.84 % | 6.6 | 3.6 h |
| Radeon (bench rig) | 2.33 GH/s | 1.17 % | 4.2 | 5.8 h |
| Weak iGPU *(illustrative)* | 0.20 GH/s | 0.10 % | 0.36 | 67 h |

The first three hashrates come from the table in the `blake3.cl` header, which
holds exactly five entries (RX 580, Vega 56, RTX 4070, Tesla V100, V100S). The
bench rig figure is our own, from a thermally controlled run (mean 2.34 GH/s
over five passes; the same card heat-soaked falls to 1.57). **The iGPU row is
illustrative, not measured** — it marks the low end of §2.3's threshold.

### 2.3 Decision: no mining pool

Three facts, each sufficient on its own.

1. **There is little variance to smooth.** A pool exists to reduce variance. At
   200 GH/s a card finds one block per day at 0.56 GH/s and one per week at
   0.08 GH/s; "several blocks per day" needs about 1.7 GH/s. A pool would help
   only cards below that, and none of the measured ones qualify.
2. **`monetarium-stratum` is not that kind of pool.** Searching its Go sources
   for `payout|pplns|payment|balance|credit|accounting` returns zero matches
   (the single repository-wide hit is prose in `docs/node-requirements.md`
   about the node's own payout address). Its README calls it "a **solo**
   stratum mining pool": a work distributor for one owner's farm. Turning it
   into a public pool means building payout accounting and taking custody of
   other people's money.
3. **A public pool here is a centralization hazard.** Seven RTX 4070s amount
   to 52 % of the network. The `--blocksubmitdivisor` flag in
   `monetarium-stratum` exists precisely as a voluntary self-limiter.

Rejected alternative: point public miners at one shared node over `getwork`.
This does not work — the template carries a coinbase paying the `miningaddr`
of whichever node served it, so all rewards go to that node's owner. **This
conclusion governs §3.7: the miner must own the node it mines against.**

### 2.4 Decision: OpenCL only, no CUDA

Three of the five measurements in the `blake3.cl` header were taken on nVidia
cards (RTX 4070 — 14.85 GH/s, Tesla V100 — 13.89, V100S — 14.60). nVidia ships
OpenCL in its driver. One kernel covers both vendors.

`blake3.cu` carries no measurements at all. That does not prove CUDA is
slower; it proves OpenCL on nVidia has been measured and works.

For public distribution, the cost of CUDA is not lines of code but a build
step on the user's machine: `cudevice.go` carries a `go:generate` that runs
`cuda_builder.go` through nvcc, plus a separate build tag, a second artifact,
and a manual Windows build document.

**Gate for revisiting:** one honest measurement of `blake3.cl` against
`blake3.cu` on the same nVidia card. A gap under 15 % closes the question
permanently. A larger gap leads to "CUDA as an optional external helper", not
back to two artifacts.

## 3. Design

### 3.1 The defining property: this design is mostly deletion

```
                              before   after   note
cl/                             4195  →  250   dlopen shim over 19 functions
stratum/                        1008  →    0   solo only
cladldevice.go                   679  →    0   80 % copy of cldevice.go
cudevice.go                      593  →    0   OpenCL covers nVidia
cuda_builder.go                  349  →    0   removes nvcc from the build
nvml/                            195  →    0   fan control not needed
adl/                              55  →    0   requires an SDK
device.go: fanControl            165  →    0   fan control
device.go: deviceStats + temp     80  →    0   sysfs, Linux-only — see §3.4
config.go: temptarget             50  →    0
kernel path flag + loader         30  →    0   see §3.5
stratum branches in miner/config 150  →    0
first run + node supervisor        0  →  300
                                ────────────
subtotal                        7549  →  550
untouched code                  3070  → 3070
                                ────────────
Total Go                       10 619  → 3 620   (−66 %)
```

What justifies the scale: `cl/` is a complete 4195-line OpenCL binding, while
the miner calls exactly **19 functions** from it (verified across
`cldevice.go` and `calibrate.go`). Likewise `nvml/` is 195 lines serving
**four** calls (`Init`, `DeviceGetHandleByIndex`, `DeviceTemperature`,
`DeviceFanSpeed`). Complete bindings are written when the library is the
product; here it is an internal detail of one binary.

### 3.2 The GPU access layer — the load-bearing part

**Principle: no driver dependency at link time.**

Four `#cgo LDFLAGS` directives across three files violate it:

```go
cl/cgo_flags.go:6    #cgo !darwin LDFLAGS: -lOpenCL
cl/cgo_flags.go:8    #cgo windows LDFLAGS: -LC:/appsdk/lib/x86_64
nvml/cgo_flags.go:4  #cgo !windows LDFLAGS: -lnvidia-ml
adl/adl.go:9         #cgo linux LDFLAGS: -latiadlxx -ldl
```

Each requires an SDK on the build machine and binds the executable to the
library being present **at startup** rather than at first use: a miner built
with `-lOpenCL` will not launch on a machine without OpenCL, not even to print
a useful error.

The replacement is a function-pointer table resolved at runtime. The function
set is the 19 that are actually called, derived from the call sites in
`cldevice.go` and `calibrate.go`:

```
CLGetPlatformIDs      CLGetDeviceIDs         CLGetDeviceInfo
CLCreateContext       CLCreateCommandQueue   CLCreateBuffer
CLCreateProgramWithSource                    CLBuildProgram
CLGetProgramBuildInfo CLCreateKernel         CLSetKernelArg
CLEnqueueNDRangeKernel                       CLEnqueueReadBuffer
CLEnqueueWriteBuffer  CLReleaseMemObject     CLReleaseKernel
CLReleaseProgram      CLReleaseCommandQueue  CLReleaseContext
```

`monetarium-stratum/cmd/gpuminer/cl/cl.h` is a useful starting point (75
lines, 22 typedefs) but **does not cover this set**: it carries
`clEnqueueFillBuffer` and lacks `clEnqueueWriteBuffer`, which `cldevice.go:667`
and `calibrate.go:63` call, and it lacks `CL_DEVICE_TYPE_ALL`, which
`cldevice.go` passes to `clGetDeviceIDs`. Its loader, `cl/host.cpp`, is
Linux-only; the macOS path exists there only as an unapplied proposed patch.
Treat it as a reference, not as a component.

The author of `adl.go` recorded this approach as a TODO in 2016 and never did
it: `// XXX we should really be using dlopen/LoadLibrary like C miners do`.

**Library search must be hardened.** Loading by bare name is a
library-planting vector, and the distribution is an archive the user unzips
into `Downloads`: on Windows, `LoadLibraryW(L"OpenCL.dll")` searches the
application directory first, so any co-extracted `OpenCL.dll` is loaded into
the process holding the node's RPC credentials. Use
`LoadLibraryExW(..., LOAD_LIBRARY_SEARCH_SYSTEM32)` on Windows and absolute
candidates on Linux. Ship `--opencl-lib <path>` as the escape hatch for
installations the table misses (xmrig shipped one after issue #1975, where
the library was installed only as `libOpenCL.so.1`); it costs about six lines
and converts an unreproducible support ticket into a one-line workaround.

```
Linux     /usr/lib/x86_64-linux-gnu/libOpenCL.so.1, then libOpenCL.so.1, libOpenCL.so
Windows   OpenCL.dll via LOAD_LIBRARY_SEARCH_SYSTEM32
macOS     /System/Library/Frameworks/OpenCL.framework/OpenCL   (see §6, open question)
```

**cgo may be avoidable entirely — measure before building the shim.**
`github.com/ebitengine/purego` (Apache-2.0) does `Dlopen`/`Dlsym` with no cgo
and has tier-1 support for Linux, macOS and Windows on amd64 and arm64. If it
holds, the cross-compilation limitation below disappears and so does the
three-runner CI matrix: `CGO_ENABLED=0 GOOS=windows go build` from any
machine. Two risks to measure rather than assume:

- purego cannot pass structs by value on some architectures. Our function set
  contains no image APIs, so `cl_image_format` never appears — confirm, then
  stop worrying.
- The hot loop makes a long **blocking** call: `cldevice.go:687` reads with
  `CL_TRUE`. Under cgo the runtime knows it is in a C call and can release the
  P; purego's trampolines may pin the OS thread. One goroutine per GPU with
  `GOMAXPROCS >= cards` is probably fine, but this is the failure that appears
  on a six-card rig and never on a laptop, and we have no six-card rig.

This is step 1 of the work order, ahead of the shim, because writing the cgo
shim and then discarding it costs more than the measurement. One phase: run
the full mining loop through purego and compare against the reference
hashrate. That exercises all 19 functions and answers the parity question at
the same time.

**If purego does not hold**, the same 19-function table is written once over
cgo instead. Two of this section's four payoffs survive either way — no SDK on
the build machine, and a binary that starts without OpenCL present to report
the problem. What is lost is `CGO_ENABLED=0`: cross-compiling then needs the
three-runner CI matrix of step 6 rather than one build from a laptop.

### 3.3 Devices: no build tags remain

Today `Device` is a single type with three mutually exclusive definitions:

```
device.go       untagged    methods on *Device, including Run and SetWork
cldevice.go     opencl      struct Device + runDevice
cladldevice.go  opencladl   struct Device + runDevice   ← 80 % copy
cudevice.go     cuda        struct Device + runDevice
```

Building without a tag fails with `undefined: Device` — the compiler stops at
the missing type on the receiver declarations in `device.go`, `miner.go` and
`calibrate.go`, before reaching any field. This is polymorphism via the
preprocessor, and its consequence is that two backends can never live in one
binary.

Since OpenCL-only is accepted, exactly **one** backend remains and the problem
disappears without introducing an interface:

```
cldevice.go     → device_opencl.go, tag removed
cladldevice.go  → deleted
cudevice.go     → deleted
```

A `Device` interface is deliberately **not** introduced: a single
implementation does not need the abstraction, and the hot loop does not pay
for dispatch. The file rename is part of the step, not a follow-up.

### 3.4 Fan control and temperature: both removed

```
device.go:173-325    fanControl()
device.go:327-336    fanControlSupported()
device.go:412-427    UpdateFanTemp()
device.go constants  ADLFanFailSafe, AMDGPUFanFailSafe, AMDGPUFanMax,
                     FanControlHysteresis, FanControlAdjustment*, Severity*,
                     TargetLower/Higher/None, ChangeLevel*, DeviceKind{ADL,NVML}
cldevice.go:286-336  deviceStats and its sysfs helpers
config.go            --temptarget, --experimental and their parsing
monitor.go:32-33     fanPercent and temperature in the status JSON
adl/, nvml/          entire packages
```

Temperature goes with the fans: the sysfs implementation is Linux-only, so on
Windows and macOS the fields read zero for every vendor. `nvidia-smi`,
`rocm-smi` and the OS's own tools do this properly.

### 3.5 A self-contained binary

```go
config.go:29     defaultClKernel = "blake3.cl"      // relative path
cldevice.go:433  loadProgramSource(cfg.ClKernel)    // read from disk at startup
```

Launching from any directory other than its own fails at kernel compile time,
which is a blocker for "download and run". The kernel moves into `go:embed`
and **`--kernel` is deleted along with `loadProgramSource`**: an override for
a file that must not change is unrequested flexibility, and a substituted
kernel is invisible to the status output for the reason given in §3.7.
Rebuilding takes seconds.

### 3.6 Work source

```
benchmark (-B)   zero-valued work.Work, needs no node   kept as the test rig
solo             websocket getwork + NotifyWork         the only production path
stratum          deleted
```

A `WorkSource` interface is not introduced — there is one production
implementation.

Deleting `stratum/` also deletes a latent bug: it sends a 4-byte extraNonce2
where the pool requires exactly 8, so every share would be rejected.

**Nonce separation across cards is kept unchanged, and it uses `Nonce3Word`.**

```go
// device.go:145-151 — one byte of Nonce3Word carries the device id,
// which is what raises the limit from 256 to 65536 devices.
if d.work.IsGetWork {          // IsGetWork is exactly solo mode
    deviceID := uint8((uint32(d.index) + uint32(randDeviceOffset2)) % 256)
    d.work.Data[128+4*work.Nonce3Word] = deviceID
}
```

`Nonce3Word` is load-bearing in solo mode and unused only in pool mode.
**Three** bytes of it are spare, not four; reusing the fourth breaks
requirement 5. `initNonces` (`device.go:66-101`) seeds the rest from
`crypto/rand`.

### 3.7 First run, node ownership, and packaging

The distribution is an archive of two files:

```
monvark   the miner; it also configures, starts and supervises the node
mond      the node, started as a child process
```

There is deliberately no third launcher binary. There is also **no mode for
attaching to an already-running external node.** §2.3 established that a
`getwork` template pays the `miningaddr` of whichever node served it and that
the miner cannot see that address; attaching to a node we did not configure
therefore hands the payout to whoever owns it. Nobody asked for that mode, and
removing it deletes the entire class of failure — an orphaned `mond` left on
the default port after a crash, or a hostile one planted there, is no longer
something to detect.

**The node is ours by construction.** `monvark` starts `mond` with an explicit
`--rpclisten` on a loopback port it chooses and an `--rpccert` under
`~/.monvark/`, rather than the defaults `127.0.0.1:9509` and
`~/.monetarium/rpc.cert`. There is then no well-known port to hijack and no
shared certificate file to replace.

On first run `monvark` asks **one** question — the payout address — and
validates it itself with `stdaddr.DecodeAddress` against the active
`chainParams` **before writing anything**, so a typo or a testnet address on
mainnet is reported in the miner's own words rather than surfacing as `mond`
exiting during a progress display. It then generates:

```
generate=0            mandatory: a node with generate=1 refuses getwork
                      ("getwork polling is disallowed while CPU mining is enabled")
miningaddr=<address>  the only question put to the user
rpcuser/rpcpass       randomly generated
rpclisten/rpccert     chosen by monvark, under ~/.monvark/
```

The config file is written `0600` and `monvark` refuses to start if it is
group- or world-writable: every field in it is parseable back into the config
struct, including `rpcserver` and `rpccert`, so write access to that file is
write access to the payout.

**First run is not the only run.** If a config already exists, `monvark` does
not ask the question again and does not rewrite the file — it reports the
payout address it found and starts. A conflicting file (a `miningaddr` that
differs from one supplied on the command line, or `generate=1`) is an error
that names the file, never a silent overwrite. This is the install-versus-
upgrade distinction from `decred/decred-release`'s `cmd/dcrinstall`, and it is
the bug that otherwise ships in v2 when a user re-extracts the archive over an
existing installation.

It then:

1. starts `mond` as a child process;
2. waits for sync, gating on `!InitialBlockDownload` from
   `getblockchaininfo` and rendering `VerificationProgress` as the progress
   bar. Both fields exist in `chainsvrresults.go`. Gate on the boolean, not on
   `blocks >= headers`: on a clean machine both are 0 before any header
   arrives, which reads as "synced" and mines at height 1;
3. **asserts once that the coinbase pays the user.** After the gate, call
   `getblocktemplate` and compare the coinbase output script with
   `stdaddr.DecodeAddress(userAddress, params).PaymentScript()`. This is the
   only point in the system where the payee is ever verified; without it the
   miner has no evidence at all about the destination of the 32 VAR per block
   it produces. Refuse to mine on mismatch;
4. begins mining, displaying the payout address persistently in the status
   output and in `monitor.go`'s JSON. `CoinbaseMaturity` is 256, so at the
   measured interval the first coin is not spendable for ~17 hours on top of a
   0.9–5.8 h mean gap: the user cannot otherwise confirm they were paid for
   most of a day;
5. shuts `mond` down when it exits.

Restart-on-crash is out of scope for v1: one crash means one manual restart.
When `mond` dies, `monvark` exits rather than continuing to hash.

**Work must expire.** The sync gate protects only the first minute.
`miner.go` calls `GetWork` once at construction; thereafter work arrives only
through the pushed `OnWork` callback, and the push path in the node
(`rpcwebsocket.go` `notifyWork`) carries neither of the guards
`handleGetWork` has (`rpcserver.go:4741` peer count, `:4754` `IsCurrent`).
Meanwhile `device.go:104-112` keeps the current work forever if no new work
arrives, and nothing in the repository consults `TimeReceived` for staleness.
A miner whose websocket dropped, or whose node's tip froze, grinds a dead
header at full reported hashrate — the candidate-count metric cannot tell the
difference. Therefore: if no new work has arrived within a bounded time,
stop the devices and re-issue `GetWork`, which is the guarded path, and
surface the condition.

**Packaging.** One archive, published through GitHub Releases with its SHA256
in the release notes. A signed manifest with a pinned key (the
`decred/decred-release` shape) is deliberately not adopted: that design exists
because components are downloaded separately, whereas both of our files arrive
as one artifact over HTTPS from one release page.

The objection that running a full node is burdensome is answered by
measurement: 312 MB, 31 023 blocks, sync takes minutes. Every miner also
becomes another full node, which for a chain this size is a contribution.

## 4. Verification

This is a money path, so verification is mandatory.

**Host/GPU agreement, using the oracle that already exists.** The kernel takes
no target: its arguments are the output buffer, the midstate `cv0..cv7` and
the message words `m0..m12`. It filters on `if (v7 ^ v15) return;`
(`blake3.cl:154`) — a fixed comparison against zero, not against the network
target, which the host applies later. So **every candidate the GPU emits has a
hash whose final 32-bit word is zero**, and `device.go:349` already recomputes
that hash host-side with `blake3.FinalBlock(d.midstate, data[128:180])`.

The test is therefore: run the device, take a candidate, and assert its
host-recomputed hash ends in a zero word. At 2.33 GH/s one arrives every
`2^32 / 2.33e9` ≈ 1.8 s, so this is a fast test, not a soak. A miscompiled
kernel, a wrong midstate, or a byte-order slip breaks the agreement
immediately.

No kernel edit, no build variant, no fixed vector to grind offline. This
tests the **compiled** kernel end to end, which catches a class the host
implementation alone cannot: a kernel that builds differently on a particular
driver.

**Host side, independently.** Test `blake3/` against `lukechampine.com/blake3`
rather than against itself. `monetarium-stratum/cmd/gpuminer/midstate_test.go`
shows the shape, but it is **not** reusable as the golden vector: it is a
pure-Go test that ports a *different* kernel's compress function, computes its
expected value at runtime rather than fixing a vector, launches no GPU, and
depends on a module absent from our `go.mod`.

**Payee verification.** The coinbase assertion of §3.7 step 3 is part of the
verification story, not an implementation detail: it is the only check that
covers where the money goes rather than whether the hash is right.

**Cheap additional checks:**

- `dlopen` against a missing library yields a clear error naming the library,
  not a panic; a resolved library with a missing symbol names the symbol;
- two devices in one process receive different `Nonce3Word` device ids. Note
  this is the honest claim: `initNonces` seeds from `crypto/rand`, so ranges
  are separated probabilistically, not partitioned. A test asserting
  provable non-overlap would be false;
- mining does not start before the sync gate passes.

**Regression metric:** hashrate on the reference rig, 2.33 GH/s on the Radeon,
checked after each of steps 2, 3 and 4 — the three steps that touch the mining
path. It is the only quantity a refactor can break silently.

## 5. Work order

Steps 2–4 are almost entirely deletion; step 1 exists to avoid writing code
that step 2 would throw away.

| # | Step | Check |
|---|---|---|
| 1 | purego spike: full mining loop, no cgo | measured parity, or a clear no |
| 2 | Remove CUDA, ADL, stratum, fan control, temperature, `nvml/` | `go build -tags opencl`, parity |
| 3 | `cl/` → runtime-loaded shim (purego or cgo per step 1) | `go build -tags opencl`, parity |
| 4 | Drop build tags; `cldevice.go` → `device_opencl.go`; `go:embed` the kernel; delete `--kernel` | `go build ./...` **untagged**, runs from an unrelated directory, parity |
| 5 | **Rename to `monvark`** (see §8) | `go build ./...`, `~/.monvark/` |
| 6 | Windows in the loader + CI matrix | artifacts out of CI (one, if step 1 succeeded) |
| 7 | First run, node ownership, sync gate, coinbase assertion, work expiry | clean VM, binaries copied in by hand: address → sync → mining |
| 8 | Build `mond`, assemble the archive, publish SHA256 | clean VM, nothing but the downloaded archive: download → address → mining |

Note that step 3's check is still `-tags opencl`: the tag comes off in step 4,
so an untagged build cannot succeed before then. Step 5 sits where it does
because the import surface is at its minimum after steps 2–4 and no public
artifacts exist yet; §8.2 justifies the window.

## 6. Open questions and out of scope

**Two questions only the owner can answer.** Both affect scope, and one of
them removes work:

1. **Windows code signing.** Unsigned miner binaries downloaded from GitHub
   are quarantined by Defender and SmartScreen largely by category rather than
   behaviour. Requirement 1 is "download and run"; either a signing
   certificate is budgeted or the support load is. This is not a design
   decision but it is a distribution cost the design assumed away.
2. **Is macOS a shipped artifact or only a development target?** Apple has
   deprecated OpenCL, and §2.4 aims at AMD and nVidia rigs. If macOS is a dev
   target only, the framework row leaves §3.2's table and notarization leaves
   step 6 entirely.

**Out of scope:**

- **A pool with payouts.** A separate project with its own spec; payout
  accounting is larger than the miner itself.
- **CUDA.** Revisited only after the measurement in §2.4, and then as an
  optional external helper rather than a second artifact.
- **Kernel optimization.** The algorithm is frozen. Any discussion of
  exceeding the current hashrate comes after everything above builds and runs.
- **Deeper de-Decred renaming.** §8 changes the module path, the binary name
  and the config directory. Internal package names and identifiers keep their
  heritage.
- **Restart on crash**, and **rig-manager monitoring APIs** (ethminer's
  Claymore-compatible `miner_getstat1`, which HiveOS speaks) — cheap to add
  later over `monitor.go`'s existing structs, irrelevant for solo v1.

## 7. Documents that go stale

- `README.md` and `sample-gominer.conf` — they describe `dcrd`, `~/.dcrd` and
  port 9109;
- `.github/workflows/go.yml` — builds against Go 1.20/1.21 while `go.mod`
  requires 1.23, and `apt-get install`s the CUDA toolkit on a single Ubuntu
  runner. **CI is red today**, so step 6 starts from broken, not from green;
- `docs/cuda-manual-windows-build.md` — deleted along with CUDA;
- `CLAUDE.md` — after step 4 the build-tag section becomes meaningless; after
  step 5 so do the paragraph on preserving names (`Do not "finish" the
  rebrand unless asked`) and the description of the git layout.

Outside this repository, the workspace `CLAUDE.md` lists `dcrdata/`,
`cryptopower/` and `decrediton/` as local checkouts that are not on disk, and
omits `monetarium-stratum/`, `monetarium-vsp/` and `tg-avatar/`, which are.
Noted, not owned by this design.

## 8. Name, repository and license

### 8.1 Decision

```
name           monvark
repository     github.com/edshav/monvark   (personal, public)
module         github.com/edshav/monvark
binary         monvark
config dir     ~/.gominer/  →  ~/.monvark/
```

`vark` is from aardvark: it digs faster than any other mammal. The `mon`
prefix groups the miner with `mond` and `monctl` in `ls` and tab completion.
The project is personal, not under the `monetarium` organization.

### 8.2 Why the rename lands on step 5

The cost of a rename is driven by the number of machines that already hold
something, not by lines of code. The window after step 4 is the only moment
when both are minimal:

- after steps 2–4, `stratum/`, `nvml/` and `adl/` are gone and `cl/` has been
  rewritten, so the import surface is at its smallest;
- steps 6–8 create public artifacts and per-user config directories, after
  which a rename requires migration.

**Hard deadline: before the first tag.** `proxy.golang.org` caches a published
module version permanently and does not allow deletion. The name must be final
before the first `git tag`, not before the first commit.

### 8.3 Scope of the work

```
work     8 imports   →  github.com/edshav/monvark/work
util     7           →  .../util
cl       3           →  .../cl        (rewritten anyway, §3.2)
blake3   1           →  .../blake3
──────────────────
        19 lines     + go mod edit
```

Five further imports across three packages (`stratum` 3, `nvml` 1, `adl` 1)
disappear with those packages in step 2.

The note in `CLAUDE.md` that the names are "load-bearing across `stratum/`,
`util/`, `work/`, `blake3/`, `cl/`, `nvml/` and `adl/`" no longer holds after
steps 2–4: three of the seven packages are deleted and a fourth is rewritten.

The pinned `master` branch loses its remaining purpose. It was kept so that
`git diff master..monetarium-retarget` is the Monetarium delta, but upstream
`decred/gominer` has not moved since January 2024 and its HEAD **is** our fork
point `68791b0`. There is nothing left to track, and this design rewrites
two thirds of the tree regardless.

### 8.4 License: obligation and asset

The repository is under **GPL-3.0** (`LICENSE`), not the ISC license used by
most Decred projects.

**Obligation:** distributing binaries obliges us to provide source. A public
repository satisfies this automatically; the obligation returns in full if the
repository is ever made private, at which point every build needs an
accompanying written offer of source.

**Asset:** GPLv3 also makes the miner ecosystem *copy-eligible* rather than
merely readable — xmrig (GPLv3), ethminer (GPLv3), cgminer (GPLv3) and
alephium/gpu-miner (LGPL-3.0) can be ingested directly. xmrig's
`src/backend/opencl/wrappers/OclLib.cpp` is the reference implementation of
runtime-loaded OpenCL and the direct prior art for §3.2. This is one-way:
nothing flows back to an ISC-licensed Decred repository.

### 8.5 Attribution left untouched

- `blake3.cl` is untouched, header and measurement table included;
- every `Copyright (c) ... The Decred developers` and `... The btcsuite
  developers` header stays in place — 20 and 4 files respectively;
- `LICENSE` is not modified.

The GPL requires preserving copyright notices, not the project name.

### 8.6 Registries

Create `github.com/edshav/monvark` now: for Go the repository URL *is* the
module path, so creating the repository is claiming the name. `pkg.go.dev` and
`proxy.golang.org` need no action — they populate on first `go get`, under the
irreversibility deadline in §8.2. The name is clean within Go: zero
repositories on GitHub, zero modules on pkg.go.dev.
