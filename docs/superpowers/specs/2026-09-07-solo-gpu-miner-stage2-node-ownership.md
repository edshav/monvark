# Design: Monetarium solo GPU miner — stage 2: node ownership, first run and release

**Status:** approved design, not yet implemented.
**Date:** 2026-09-07.
**Supersedes:** §3.7, §4 "Payee verification" and §5 steps 7–8 of
`2026-09-06-solo-gpu-miner-design.md`. Everything else in that document still
stands and is not repeated here.
**Predecessor:** `../plans/2026-09-06-solo-gpu-miner-stage1-miner.md`, complete.
Its "Stage 2 preview" table of forward constraints is the input to this design
and every row of it is honoured below.

---

## 1. Goal and scope

Stage 1 produced a miner that hashes correctly and ships as one self-contained
binary, but that still expects a node somebody configured by hand, and that
verifies nothing at all about where its 32 VAR per block goes.

Stage 2 closes that: `monvark` asks one question, owns its node, refuses to
mine on a chain it has not caught up with, proves on-chain that the coinbase
paid the user, notices when its work has gone stale, and ships as an archive a
user unpacks and runs.

**In scope:** first run and the payout address; `mond` generation, supervision
and shutdown; the sync gate; payee verification; work expiry; packaging and the
release workflow; the README changes all of that implies.

**Out of scope, deliberately:** restart-on-crash, an external-node mode,
multi-address payouts, rig-manager monitoring APIs, CUDA, kernel optimisation,
deeper de-Decred renaming. All were ruled out in stage 1's spec §6 and nothing
here reopens them.

---

## 2. Decisions taken in this stage

Six decisions were made during design. Each replaces something the stage 1 spec
either got wrong about the node's actual API or left as an open question.

**2.1 Payee verification is construction plus post-hoc proof.**
Spec §3.7 step 3 called for `getblocktemplate`. That RPC does not exist on
`monetarium-node` (§7.1). More importantly, the check as specified could not
have worked as a security control anyway: §3.7 already makes `mond` a child
process shipped inside our own archive, so the only remaining threat to the
payout is a substituted or tampered `mond` — and such a node lies freely on
every RPC it serves, `getblocktemplate` included.

So the guarantee is split. **Construction** carries the security: the payout
address is passed on the node's own command line, never through a file.
**Post-hoc proof** carries the evidence: once a block we submitted is accepted,
monvark reads it back from the chain and asserts the coinbase pays the user,
refusing to continue on a mismatch. The README states plainly that this catches
configuration faults and gives the user chain evidence, and that it is not a
defence against a substituted node binary — the archive's SHA256 is.

**2.2 The node binary comes from a pinned, checksummed upstream release asset.**
`monetarium-node` already cross-compiles and publishes exactly the two platforms
we ship (§7.6). The release workflow downloads those assets at a pinned tag,
verifies a SHA256 hardcoded in the workflow, and renames them into the archive.
A changed upstream asset fails the build loudly rather than shipping silently.

**2.3 Windows ships unsigned.** No code-signing certificate is budgeted
(stage 1 spec §6 question 1). The consequence is documented rather than
engineered around; see §3.10.

**2.4 Credentials live in a 0600 file; identity lives on argv.**
`~/.monvark/mond.conf` holds only `rpcuser`, `rpcpass`, `rpclisten`, `rpccert`
and `rpckey`, so the RPC password never appears in `ps`. `--miningaddr` is
passed on the node's command line and must never also appear in a file — see
§3.4, which explains why this is a correctness requirement and not a preference.

**2.5 Work expiry polls and keeps hashing; it never stops the devices.**
Stage 1 spec §3.7 said "stop the devices and re-issue `GetWork`". Stopping is
wrong twice over. The poll's own result already distinguishes the two cases it
was meant to disambiguate, and a quiet chain routinely produces gaps long enough
to trip any useful bound (§7.4). It is also unbuildable without new device
state: `SetWork` is the only path into a device and it always sets `hasWork`
true, so there is no way to un-arm one from outside. A persistently failing
`GetWork` is handled by the policy that already exists for an unhealthy node —
warn, retry, and eventually exit.

**2.6 The GPU is proven working before anything else happens.**
Device construction moves out of `NewMiner` and to the top of `main.go`, ahead
of the payout question and the node. A broken driver then fails in seconds
rather than after a full chain sync. This is a change to the startup order that
the stage 1 spec did not anticipate; §3.1 records why it is the full device
build and not a cheap enumeration.

**2.7 An already-running node is coexisted with, never killed and never
auto-attached to.** A machine may already run a node for `monetarium-explorer`
or for a wallet in RPC sync mode. monvark starts its own regardless, taking an
ephemeral P2P port if the default is busy (§3.4).

Killing the other node was considered and rejected outright: its ownership
cannot be determined, killing it may not even be permitted, monvark would never
restore it, and it solves nothing the port fallback does not solve without
destroying anything.

Auto-attaching to it was rejected for a sharper reason. Five of its six failure
modes are loud and therefore harmless — the node's RPC server is disabled unless
credentials are configured and that is the default (§7.10), its credentials live
in its own config file, `generate=1` makes it refuse `getwork`, it may be on
another network, and it may shut down mid-session. The sixth is silent: a
`getwork` template pays the `miningaddr` of whichever node served it, no RPC
exposes that address (§7.1), and our payee proof is post-hoc. An auto-attached
node belonging to somebody else would therefore consume hours of hashrate before
the first mined block revealed it. Auto-attaching means silently guessing about
the destination of the money, which is the one thing this design exists to
settle.

---

## 3. Design

### 3.1 Startup sequence

Every addition is an insertion into `main.go`. `NewMiner` gains one parameter
and is otherwise unchanged; `Run` keeps its shape.

```
loadConfig()                      existing; cl.Load already happens here
devices := newMinerDevs(...)      MOVED here from inside NewMiner
addr := resolvePayout(cfg)        NEW  payout.go
node := nodeStart(ctx, cfg, addr) NEW  node.go; returns a supervised child + an HTTP RPC client
defer node.Stop()                 NEW
node.WaitSynced(ctx)              NEW  node.go
m := NewMiner(ctx, devices)       existing, signature gains devices
m.Run(ctx)                        existing, plus expiry and the payee assertion
```

**Why devices move first.** Device setup has two distinct failure points.
`getCLPlatforms`/`getCLDevices` merely enumerate, cheaply and without creating a
context. `NewDevice` creates the context and compiles `blake3.cl`, which is
where a driver that enumerates perfectly still fails. A pre-flight that only
counted devices would therefore miss the more interesting failure, so the full
build runs first. It costs nothing at runtime: a device consumes no GPU time
until `SetWork` feeds it, so it idles through the sync either way.

**Why the payout question precedes the node.** The user answers one question
against a quiet terminal and then walks away for the sync, and nothing has been
started that would need tearing down if they abandon the prompt.

**Two RPC clients, deliberately.** `node.go` holds an `HTTPPostMode` client used
for the sync gate, the payee read-back and shutdown. `miner.go` keeps its own
websocket client for work notifications and submission, exactly as today.
Sharing one client would force `NewMiner` to accept a live connection and so
break stage 1's forward constraint that `NewMiner` assume nothing about a node
being reachable. Two single-purpose clients is both the smaller diff and the
thing the constraint asks for.

**Benchmark mode short-circuits all of it.** With `-B`, `resolvePayout`,
`nodeStart` and `WaitSynced` are skipped entirely. Benchmark remains the one
path that needs no node, which every parity and GPU check in stage 1 depends on.

### 3.2 Files and directory layout

Two new Go files. Work expiry and the payee assertion are roughly thirty lines
each and live in `miner.go`, where the RPC client and the submit path already
are; a file apiece would be scaffolding.

```
payout.go   the address: resolution, validation, persistence
node.go     mond.conf generation, child supervision, sync gate, shutdown
```

Runtime layout, all of it under a directory `loadConfig` already creates at
0700:

```
~/.monvark/
  monvark.conf   0600   miningaddr=<address>, plus whatever else the user set
  mond.conf      0600   rpcuser rpcpass rpclisten rpccert rpckey — written by us
  rpc.cert              generated by the node on first start
  rpc.key               generated by the node on first start
  node/                 the node's --appdata: block data, logs
```

Distribution layout, unpacked wherever the user chose:

```
monvark   or monvark.exe
mond      or mond.exe
```

### 3.3 First run and the payout address

**Precedence** is `--miningaddr` on the command line, then `miningaddr` in
`~/.monvark/monvark.conf`, then an interactive prompt.

`loadConfig` already parses the ini file into the config struct and then
re-parses the command line so flags win (`config.go`, "Parse command line
options again to ensure they take precedence"). Adding a `MiningAddr` field with
`long:"miningaddr"` therefore makes both sources work with no new parsing. To
detect a *conflict* — which the merged value cannot show — `loadConfig` captures
the field's value in a local between the ini parse and the second command-line
parse. That local is the file's value; the field afterwards is the effective
one. Both non-empty and different is an error naming the file. This costs one
assignment and no second parse.

**Validation happens before anything is written.** `stdaddr.DecodeAddress`
against the active `chainParams`, so a typo, or a testnet address supplied on
mainnet, is reported in the miner's own words rather than surfacing later as the
node exiting during a progress display.

**Rules.**

- A conflict between flag and file is an error that names the file. The file is
  never rewritten to match, and never silently overwritten. This is the
  install-versus-upgrade distinction that otherwise ships as a bug when a user
  re-extracts the archive over an existing installation.
- A value that came from the prompt is appended to `monvark.conf`, creating it
  0600 if absent. A value that came from flag or file is not written back.
- monvark refuses to start if `monvark.conf` is group- or world-writable. Every
  field in it parses back into the config struct, so write access to that file
  is write access to the payout.
- No flag, no file entry, and stdin is not a terminal: an error telling the user
  to pass `--miningaddr`. This is the path systemd and Docker take.

**The prompt names where an address comes from.** monvark's first question
cannot be answered by anyone who has not already set up a wallet, so the prompt
and the README's opening both point at `monetarium-wallet`. monvark never
generates a key: a private key on a mining rig is precisely what this design
avoids.

**The wallet is not a runtime dependency, and the README says so.** monvark
needs an address *string*, not a running program. The wallet is used once, on
any machine, to produce it; it does not need to run during mining and should not
be installed on the rig at all, since keys on a machine running unattended GPU
workloads is the exposure this design is built to avoid. The complete
requirement list for a fresh rig is an OpenCL driver and one string obtained
elsewhere — no node to install, no wallet to run, no service to configure. That
question is invited by the design and belongs in the README's first paragraph
rather than being discovered at the prompt.

### 3.4 Node ownership

**Locating the binary.** `mond`, or `mond.exe` on Windows, in the same directory
as `os.Executable()`. A `--mond` flag overrides the path for development. The
error when it is absent names the path that was searched. Note that upstream
does not call it `mond` — `go build .` in the node repository yields
`monetarium-node`, and its release assets are `monetarium-node-<goos>-<goarch>`
(§7.6). The name `mond` is one we impose when assembling our own archive, which
is why the lookup rule is written down rather than assumed.

**The RPC port** is chosen by binding `127.0.0.1:0`, reading the assigned port,
and closing the listener before the node binds it. There is a small window
between close and bind in which another process could take the port; it is
local, brief, and the standard way to do this. The node failing to bind is a
clear startup error, not a silent misbehaviour.

**The P2P port must be de-conflicted too, or an existing node blocks startup.**
`initListeners` warns and skips a P2P address it cannot bind, but the caller
then fails hard if every address failed — `errors.New("no valid listen address")`
(§7.11). The node's default P2P port is 9508 on all interfaces, so a machine
already running a Monetarium node would kill monvark's child at startup.

monvark therefore test-binds the default P2P port for the active network before
launching:

- **free** — pass no `--listen`, so the node listens on 9508 as usual and
  contributes fully to the network;
- **busy** — pass `--listen=:0`, so the node takes a random free port, and warn
  that it is doing so. An explicit port survives `normalizeAddresses` intact
  (§7.12), and `net.Listen` on port 0 assigns a free one.

A node on an ephemeral port still dials out, syncs, validates and relays to its
outbound peers; it simply cannot accept inbound connections. That is already the
situation for any rig behind NAT without a forwarded port, which is most of
them. The user is never blocked and the other node is never touched.

**Credentials** are 32 bytes from `crypto/rand`, hex-encoded, fresh each start.
There is nothing to preserve between runs, so `mond.conf` is regenerated rather
than reused.

**Tamper detection before regeneration.** If `mond.conf` already exists it is
checked before being replaced: group- or world-writable is an error, and so is
the presence of a `miningaddr` or `generate` key. Overwriting would in fact be
safe, since our values win — but silently repairing a file somebody edited hides
exactly the event worth reporting.

**Why `--miningaddr` must be argv-only.** The node selects uniformly at random
among its configured mining addresses:

```go
payToAddr := g.cfg.MiningAddrs[rand.IntN(len(g.cfg.MiningAddrs))]
```

(`internal/mining/bgblktmplgenerator.go:728`.) `MiningAddrs` is a `[]string`, and
go-flags *appends* command-line occurrences to entries already read from the
config file rather than overriding them. So an address that ever lands in both
places produces two entries and a random fraction of blocks paying elsewhere.
Keeping the address on argv only, and refusing to start on a `mond.conf` that
contains one, is therefore a correctness requirement.

**The command.**

```
mond --configfile=~/.monvark/mond.conf \
     --appdata=~/.monvark/node \
     --miningaddr=<address> \
     [--listen=:0]          only when the default P2P port is busy
     [--testnet | --simnet]
```

`--generate` is omitted; the node's default is off, and a node with CPU mining
enabled refuses `getwork`. Passing `--configfile` explicitly also isolates us
from any pre-existing `~/.monetarium/` configuration the machine may carry.

**Output.** `cmd.Stderr` inherits monvark's, so startup failures are visible.
The node writes its own logs to `~/.monvark/node/logs/` as normal.

**Shutdown** is `RawRequest(ctx, "stop", nil)` on the RPC client we already
hold, then `cmd.Wait` under a timeout, with `Process.Kill` as the backstop.
`rpcclient` exposes no `Stop` method but does expose `RawRequest` (§7.5). This
is one code path on all three platforms; a signal-based shutdown would need a
split, because `os.Interrupt` cannot be delivered through `Process.Signal` on
Windows, and the `Kill` that would remain there risks the node's database.

**If the node dies while mining, monvark exits.** Restart-on-crash is out of
scope: one crash means one manual restart.

### 3.5 The sync gate

Poll `GetBlockChainInfo` and gate on `!InitialBlockDownload`, rendering
`VerificationProgress` as the progress display. Not `blocks >= headers`: on a
clean machine both are 0 before any header arrives, which reads as "synced" and
mines at height 1.

The gate begins with a connect-retry loop, because the node needs a moment to
start and to generate `rpc.cert` before it will accept a connection.

**The progress line carries the peer count**, from `GetConnectionCount`. This is
not decoration. `InitialBlockDownload` is `!chain.IsCurrent()`
(`internal/rpcserver/rpcserver.go:2384`), and `IsCurrent` never becomes true
without peers, so a firewalled machine otherwise sits at 0.0% forever with no
explanation. One extra field turns a silent hang into a self-explaining one.

Mining does not start before this gate passes.

### 3.6 Work expiry

`miner.go` calls `GetWork` once at construction; thereafter work arrives only
through the pushed `OnWork` callback. That push path carries neither of the
guards `handleGetWork` has, `device.go` keeps the current work forever if none
arrives, and nothing consults `TimeReceived` for staleness. A miner whose
websocket dropped, or whose node's tip froze, grinds a dead header at full
reported hashrate.

A ticker checks `now - work.TimeReceived` against a **15 minute** bound and, when
exceeded, calls `GetWork` — the guarded path:

| Outcome | Meaning | Action |
|---|---|---|
| error | the node's peer-count or `IsCurrent` guard fired | warn; on the third consecutive failure, exit |
| data differs from current | the push path had stalled | warn, adopt the new work, carry on |
| data matches current | the chain is simply quiet | debug, carry on |

The devices are never stopped, by the timeout or by anything else. The poll is
self-diagnosing: its result already separates a broken push path from a quiet
chain, so a stop would convey nothing the result does not while costing real
hashrate. A repeatedly failing `GetWork` means an unhealthy node, and monvark's
policy for an unhealthy node is already to exit — three consecutive failures is
45 minutes, well past any transient. Exiting also avoids inventing a device
pause: `SetWork` is the only way into a device and it always arms it, so a
pause would mean new state on the hot path for a condition that ends in an exit
regardless.

**Why 15 minutes.** The node regenerates a template after
`templateRegenSecs = 30` only when new transactions have arrived
(`internal/mining/bgblktmplgenerator.go:35-38`). On a chain with an empty mempool
there is no regen and therefore no push, so work arrives on new blocks alone, at
`TargetTimePerBlock = 5 minutes` and Poisson-distributed. Fifteen minutes is
three times the target, so it fires on roughly 5% of healthy gaps, and each
firing is one cheap RPC call with no user-visible effect.

### 3.7 Payee verification

The assertion runs **inline in `workSubmitThread`, on the first accepted block**.
That path already computes the block hash for its success log, so it needs
neither a set of submitted hashes nor any coupling to `OnBlockConnected`.

```
GetWorkSubmit accepted
  -> GetBlockVerbose(hash, true)          on the miner's own client
  -> want, _ := stdaddr.DecodeAddress(addr, params); wantScript from PaymentScript()
  -> assert some RawTx[0].Vout[].ScriptPubKey.Hex == wantScript
     match    -> log the height and the address, once
     mismatch -> critical log, stop mining
```

The comparison is against `ScriptPubKey.Hex`, not `ScriptPubKey.Addresses`
(§7.3). Comparing raw payment scripts is the exact check the original design
wanted from `getblocktemplate`, and it survives address-encoding variations that
string matching would not.

The call goes through `miner.go`'s existing websocket client, not `node.go`'s.
A websocket-mode `rpcclient` serves `GetBlockVerbose` exactly as an
`HTTPPostMode` one does, so using the client already in scope removes a
cross-component dependency for nothing lost.

Reading the block back immediately after acceptance is safe: the node has
already processed it, and `getblock` resolves a block by hash from the block
index even if it is subsequently reorganised out.

Per §2.1, this is evidence and a bug detector, not a defence against a
substituted node.

### 3.8 Flag surface changes

**Added:** `--miningaddr`, `--mond`.

**Demoted to internal:** `--rpcserver`, `--rpccert`, `--rpcuser`, `--rpcpass`
lose their flag tags but keep their config-struct fields, which `nodeStart` now
fills in with the values it chose. Stage 1 spec §3.7 removes the
attach-to-an-external-node mode outright, because a `getwork` template pays the
`miningaddr` of whichever node served it. Keeping the fields is what "become
internal" means and is a smaller diff than rewiring `newSoloMiner`.

**Deleted:** `--proxy`, `--proxyuser`, `--proxypass`. You do not proxy to your
own child process on loopback.

`defaultRPCCertFile`, which today resolves under the node's own
`~/.monetarium/`, is replaced by the path `nodeStart` chooses under
`~/.monvark/`. `nodeHomeDir` loses its last use and goes with it.

### 3.9 Status output

The payout address appears in the periodic stats line and in `monitor.go`'s
status JSON. `CoinbaseMaturity` is 256, so at the measured block interval the
first coin is not spendable for the better part of a day on top of the mean gap
to finding a block; without the address on screen the user has no way to confirm
they are being paid.

### 3.10 Packaging and release

A new tag-triggered `.github/workflows/release.yml`:

1. cross-compile `monvark` for linux/amd64 and windows/amd64, `CGO_ENABLED=0`,
   `-trimpath`, at the `go.mod` floor — the same settings the existing build job
   uses;
2. download `monetarium-node`'s published assets at a pinned tag and verify a
   SHA256 hardcoded in the workflow;
3. rename them to `mond` and `mond.exe`;
4. assemble **`.tar.gz` for Linux and `.zip` for Windows**. Not one format for
   both: `.zip` does not reliably preserve the Unix execute bit across unzip
   implementations, which would greet a Linux user with `permission denied`;
5. publish both archives with their SHA256 sums in the release notes.

macOS is not published. Stage 1 already settled spec §6 question 2 by treating
it as a development target and compiling it to `/dev/null` in CI to keep
`loader_unix.go` honest.

**The README gains a Windows section**, because unsigned means the first run is
three dialogs rather than "download and run": SmartScreen's "Windows protected
your PC", a Defender that may quarantine a miner binary by category, and a
firewall prompt when the node opens P2P port 9508. It also gains the SHA256
verification steps, which are what actually establishes that `mond` is ours.

---

## 4. Verification

The logic lives in pure predicates so it is testable without a node or a GPU:

- **Address resolution** — precedence, the flag/file conflict error, rejection of
  a testnet address on mainnet, the non-TTY error, the permission refusal.
- **`mond.conf` generation** — content, 0600 mode, and the tamper detection that
  rejects a file carrying `miningaddr` or `generate`.
- **Binary lookup** — the error names the path searched.
- **Port selection** — the P2P argument is absent when the default port is free
  and `--listen=:0` when it is busy. Testable by holding the port in the test.
- **Sync-gate predicate** — over a synthesized `GetBlockChainInfoResult`,
  including the `blocks == headers == 0` case that must *not* read as synced.
- **Expiry decision** — the three-way classification of §3.6 as a pure function.
- **Coinbase check** — over a synthesized `GetBlockVerboseResult`: pays us, pays
  someone else, and a coinbase with several outputs of which one is ours.

**End to end**, per stage 1 spec §5 steps 7–8: a clean VM with nothing but the
downloaded archive, taken from download through the address question and the
sync to mining.

**Regression metric** is unchanged and unchanged in method: stage 2 does not
touch the mining path, so the static check from the repo's `CLAUDE.md` applies —
`blake3.cl` unchanged, `-D WORKSIZE=64` unchanged — rather than a hashrate
comparison, which this rig cannot make reliably.

---

## 5. Work order

| # | Step | Check |
|---|---|---|
| 1 | Move device construction ahead of everything; `NewMiner` takes devices | `-B` still runs; a machine with no driver fails in seconds |
| 2 | `payout.go`: resolution, validation, persistence, permissions | unit tests; a real first run |
| 3 | `node.go`: `mond.conf`, port selection, child start, shutdown | node starts and stops cleanly; `mond.conf` is 0600; starts alongside a node already holding 9508 |
| 4 | `node.go`: connect-retry and the sync gate | mining does not start before the gate passes |
| 5 | Payee assertion in `workSubmitThread` | simnet: mine a block, assert the coinbase; then flip the address and assert it refuses |
| 6 | Work expiry in `miner.go` | unit test on the decision; kill the websocket and watch it recover |
| 7 | Flag surface and status output | `--help` is honest; the address appears in the JSON |
| 8 | `release.yml`, archives, README | a clean VM, nothing but the archive |

---

## 6. Open items

None blocking. Both of stage 1 spec §6's owner questions are now answered:
Windows ships unsigned and documented (§2.3), and macOS is a development target
(§3.10).

**Considered and declined: an explicit `--rpcserver` opt-in** for attaching to a
node the user names themselves. The P2P fallback of §3.4 removes the blocker
that motivated it, so building it now would be speculative, and it would restore
four flags §3.8 deletes.

---

## 7. Facts verified against the source

Everything below was checked against `monetarium-node` at `v1.3.10`, the version
`go.mod` pins, rather than assumed.

**7.1 `getblocktemplate` does not exist.** It is absent from the handler map in
`internal/rpcserver/rpcserver.go`. The mining handlers present are `getwork`,
`getmininginfo` and `regentemplate`. `regentemplate` takes no arguments and
returns nothing — it only calls `bt.ForceRegen()`. No RPC exposes the current
template's coinbase or the node's configured mining addresses;
`GetMiningInfoResult` carries `Generate` but no address.

**7.2 The sync gate is buildable as specified.** `rpcclient` has
`GetBlockChainInfo` (`chain.go:269`), and `GetBlockChainInfoResult` carries both
`VerificationProgress` and `InitialBlockDownload`
(`rpc/jsonrpc/types/chainsvrresults.go:177,179`). `InitialBlockDownload` is set
from `!chain.IsCurrent()` (`internal/rpcserver/rpcserver.go:2384`).

**7.3 The coinbase can be compared by script.** `rpcclient.GetBlockVerbose`
exists (`chain.go:158`); `TxRawResult.Vout[].ScriptPubKey` is a
`ScriptPubKeyResult` carrying `Hex` as well as `Addresses`; and
`stdaddr.Address` exposes `PaymentScript() (uint16, []byte)`
(`txscript/stdaddr/address.go:34`).

**7.4 Work push cadence.** `templateRegenSecs = 30`
(`internal/mining/bgblktmplgenerator.go:38`) but regen requires new
transactions, and `TargetTimePerBlock` is `time.Minute * 5`
(`chaincfg/mainnetparams.go:100`).

**7.5 Shutdown.** `rpcclient` has no `Stop` method; it has
`RawRequest(ctx, method, params)` (`rawrequest.go:78`).

**7.6 Release assets.** `monetarium-node`'s `release.yml` builds
`monetarium-node-<goos>-<goarch>` and its `v1.3.10` release publishes
`darwin-amd64`, `darwin-arm64`, `linux-amd64` and `windows-amd64.exe`. There is
no published checksum file, which is why the workflow pins the SHA256 itself.

**7.7 Every flag monvark needs is on the node's command line.** `--appdata`,
`--configfile`, `--rpclisten`, `--rpcuser`, `--rpcpass`, `--rpccert`,
`--rpckey`, `--miningaddr` and `--generate` all exist in the node's
`config.go`; `--generate` is a bool defaulting to off.

**7.8 Mining address selection is random.**
`payToAddr := g.cfg.MiningAddrs[rand.IntN(len(g.cfg.MiningAddrs))]`
(`internal/mining/bgblktmplgenerator.go:728`), and `getwork` errors when the
list is empty (`internal/rpcserver/rpcserver.go:4733`).

**7.9 Config precedence already works.** `loadConfig` parses the ini file into
the config struct and then re-parses the command line so flags win, which is why
`--miningaddr` needs no new parsing and why conflict detection needs exactly one
captured local.

**7.10 The node's RPC server is off by default.** `config.go:1041-1046` sets
`DisableRPC = true` when basic auth is in use and neither `rpcuser`/`rpcpass` nor
`rpclimituser`/`rpclimitpass` is configured. A running node therefore very often
has no RPC at all, which is what makes auto-detection unworkable rather than
merely unwise.

**7.11 A P2P bind failure is fatal when it is total.** `initListeners` logs
`Can't listen on %s` and continues past an address it cannot bind
(`server.go:4395-4402`), but the caller returns
`errors.New("no valid listen address")` when none succeeded (`server.go:3871`).
`DefaultPort` is `9508` (`chaincfg/mainnetparams.go:86`).

**7.12 An explicit port survives normalisation.** `normalizeAddresses` splits
each address with `net.SplitHostPort` and keeps the port it finds, substituting
the default only when there is none — so `--listen=:0` reaches `net.Listen` as
`:0`.

---

## 8. Errata

Corrections found during implementation, dated 2026-09-07.

**§3.6's "three consecutive failures is 45 minutes" is wrong.** The ticker
that drives the poll is one minute, not `workExpiry`, so once work is stale
`GetWork` is polled every minute and three consecutive failures land roughly
17 minutes into the outage, not 45. The behaviour itself is right — 17 minutes
is well past any transient — and the shutdown message was corrected to report
only what the loop actually measures, rather than repeating the wrong figure.

**§3.6's three-way table assumes `getwork` returns byte-identical data for an
unchanged template. It does not.** The node's `handleGetWorkRequest` calls
`UpdateBlockTime` before serializing on every call, so the timestamp word
always differs between polls even when the template itself has not changed.
The classification in the table is only implementable against the
template-identifying prefix `data[:128]` — version, previous block, both
merkle roots and bits — not the full 192 bytes.
