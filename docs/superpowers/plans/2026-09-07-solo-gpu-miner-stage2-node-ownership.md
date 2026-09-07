# Solo GPU Miner — Stage 2: Node Ownership, First Run and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `monvark` from a miner that expects a hand-configured node into one
that asks a single question, starts and supervises its own node, refuses to mine
on an unsynced chain, proves on-chain that the coinbase paid the user, notices
stale work, and ships as an archive a user unpacks and runs.

**Architecture:** Every addition is an insertion into `main.go` between
`loadConfig` and `NewMiner`. Two new files carry it: `payout.go` (the address)
and `node.go` (the child process, its configuration, and the sync gate). Work
expiry and the payee proof are about thirty lines each and go into `miner.go`,
where the RPC client and the submit path already live. `NewMiner` gains one
parameter — the devices, now built first — and is otherwise unchanged.

**Tech Stack:** Go 1.23, `os/exec`, `crypto/rand`, `rpcclient` and
`txscript/stdaddr` from `monetarium-node` v1.3.10, `go-flags` v1.6.1.

**Spec:** `docs/superpowers/specs/2026-09-07-solo-gpu-miner-stage2-node-ownership.md`
— read it alongside this plan; the plan argues from it. Its §7 records every
fact about the node that was verified against the source rather than assumed,
with file and line references. The predecessor spec
(`2026-09-06-solo-gpu-miner-design.md`) still governs everything stage 1 built.

**Scope:** Stage 2 of 2, covering the predecessor spec's work-order steps 7–8 and
the "Payee verification" item of its §4. Stage 1 is complete and shipped.

---

## Global Constraints

Every task's requirements implicitly include this section. Values are copied
verbatim from the specs and the repository's `CLAUDE.md`.

- **Go floor is 1.23** (`go.mod`). Do not raise it. `purego` stays pinned at
  **v0.10.2**: v0.11.0 declares `go 1.25`.
- **`CGO_ENABLED=0 go build .` must keep working** for linux/amd64,
  windows/amd64 and darwin/arm64 from one machine. No `#cgo` directive
  referencing a GPU driver may appear anywhere in the tree.
- **The mining path is not touched in this stage.** `blake3.cl` is not edited and
  the `clBuildProgram` option string stays `-D WORKSIZE=64`. Those two facts,
  not a hashrate comparison, are the regression check — the reference rig is
  thermally dominated and cannot make a reliable rate comparison across runs
  (repo `CLAUDE.md`, "Measuring hashrate").
- **`--miningaddr` is passed on the node's command line and must never also
  appear in a file.** The node picks uniformly at random among its configured
  mining addresses (`bgblktmplgenerator.go:728`) and go-flags *appends* command
  line occurrences to config file entries rather than overriding them, so an
  address in both places silently pays a random fraction of blocks elsewhere.
  This is a correctness requirement, not a style preference.
- **BLAKE256 in `miner.go` is not leftover Decred code.** The PoW hash is
  BLAKE3; the block identity hash is BLAKE256. Leave
  `chainhash.Hash(blake256.Sum256(data[:180]))` alone.
- **Benchmark mode (`-B`) must need no node.** `resolvePayout`, `nodeStart` and
  `WaitSynced` are all skipped under `-B`. It is the only way to exercise the
  mining loop without a node and every GPU check in stage 1 depends on it.
- **Licensing:** GPL-3.0. Every `Copyright (c) ... The Decred developers` and
  `... The btcsuite developers` header stays. `LICENSE` and `cl/LICENSE` are not
  modified.
- **Internal identifiers keep their `dcr*` heritage** (`dcrutil`, `chainhash`,
  `blake256`, `rpcclient`, `chaincfg`). Deeper renaming is out of scope.
- **Commit-message style is `package/path: concise description`**, and every
  commit ends with:

  ```
  Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_0188qbCHRbXP7ehWfwzukbKP
  ```

  The task steps below show `git commit -m "..."` for brevity; add the trailer
  to every one of them.
- **`./run_tests.sh` must pass** at the end of every task: `go test -v ./...`
  plus `golangci-lint run` (v2 schema, config in `.golangci.yml`).

---

## File Structure

**Created**

```
payout.go            the payout address: precedence, validation, persistence
payout_test.go
node.go              mond.conf, port selection, child supervision, the sync gate
node_test.go
.github/workflows/release.yml   tag-triggered archive assembly
```

**Modified**

```
main.go              the new startup sequence
config.go            --miningaddr and --mond added; proxy flags deleted; RPC
                     flags demoted to internal fields; fileMiningAddr captured
miner.go             NewMiner takes devices; payee proof; work expiry
monitor.go           the payout address joins the status JSON
README.md            requirements, first run, Windows, verification
sample-monvark.conf  the flags that still exist
CLAUDE.md            stage 2 is built; the "Outstanding" section is rewritten
```

**Untouched**

```
blake3.cl  blake3/  cl/  device.go  device_opencl.go  calibrate.go  work/  util/
```

---

## Task 1: Build the devices before anything else

A broken OpenCL driver currently surfaces only after the node has been started
and the chain synced, because `newMinerDevs` runs inside `NewMiner`. Moving it to
the top of `main.go` makes it fail in seconds. It must be the full build and not
a cheap enumeration: `getCLPlatforms`/`getCLDevices` only enumerate, while
`NewDevice` creates the context and compiles `blake3.cl`, which is where a driver
that enumerates perfectly still fails.

**Files:**
- Modify: `miner.go` — `NewMiner` signature and body
- Modify: `main.go` — build devices, pass them in

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func NewMiner(ctx context.Context, devices []*Device) (*Miner, error)`
  — every later task that edits `miner.go` builds on this signature.

- [ ] **Step 1: Change `NewMiner` to accept devices**

In `miner.go`, replace the opening of `NewMiner`:

```go
func NewMiner(ctx context.Context, devices []*Device) (*Miner, error) {
	var m *Miner
	var err error
	if cfg.Benchmark {
		m = &Miner{devices: devices}
	} else {
		m, err = newSoloMiner(ctx, devices)
	}
	if err != nil {
		return nil, err
	}
```

Delete these lines from the top of the old body — they move to `main.go`:

```go
	workDone := make(chan []byte, 10)

	devices, err := newMinerDevs(workDone)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("no devices started")
	}
```

`m.workDone = workDone` in the remainder of `NewMiner` no longer has a
`workDone` in scope. The channel now belongs to `main.go`, so take it from the
devices instead by adding a field. In `miner.go`, change the assignment to:

```go
	m.workDone = workDone
```

...and give `NewMiner` the channel as a second thing to receive. Rather than a
third parameter, store it on the `Miner` from `main.go`. Use this exact shape:

```go
func NewMiner(ctx context.Context, devices []*Device, workDone chan []byte) (*Miner, error) {
```

- [ ] **Step 2: Add the device construction helper to `main.go`**

Add to `main.go`:

```go
// newDevices builds every enabled OpenCL device.  It runs before the payout
// question and before the node starts, so a driver that cannot compile the
// kernel fails in seconds rather than after a full chain sync.  A device
// consumes no GPU time until SetWork feeds it, so building early costs nothing.
func newDevices() ([]*Device, chan []byte, error) {
	workDone := make(chan []byte, 10)
	devices, err := newMinerDevs(workDone)
	if err != nil {
		return nil, nil, err
	}
	if len(devices) == 0 {
		return nil, nil, errors.New("no devices started")
	}
	return devices, workDone, nil
}
```

Add `"errors"` to `main.go`'s imports.

- [ ] **Step 3: Wire it into `monvarkMain`**

In `main.go`, immediately after the version line
(`mainLog.Infof("Version %s %s ...")`), insert:

```go
	// Build the devices first.  Everything after this point can take minutes,
	// and a user whose driver is broken should not wait through it.
	devices, workDone, err := newDevices()
	if err != nil {
		mainLog.Criticalf("Error initializing devices: %v", err)
		return err
	}
```

Then change the `NewMiner` call further down from:

```go
	m, err := NewMiner(ctx)
```

to:

```go
	m, err := NewMiner(ctx, devices, workDone)
```

- [ ] **Step 4: Build and run the existing tests**

Run: `go build . && ./run_tests.sh`
Expected: builds clean; `go test -v ./...` passes, including `device_test.go`'s
GPU/host agreement test (or its skip when no OpenCL device is present).

There is no new unit test in this task and that is deliberate: it is a pure
reordering plus a signature change, and the compiler plus the existing device
tests are exactly the checks that would catch a mistake. Inventing a test that
asserts a function was called in a particular order would test the plan, not the
code.

- [ ] **Step 5: Verify the ordering by hand**

Run: `go build -o /tmp/monvark-t1 . && /tmp/monvark-t1 --opencl-lib /nonexistent -B`
Expected: fails immediately with an error naming the missing library, before any
other output. Then:

Run: `/tmp/monvark-t1 -B` for about ten seconds, Ctrl+C.
Expected: benchmark mode still runs and reports a hashrate.

- [ ] **Step 6: Commit**

```bash
git add main.go miner.go
git commit -m "main: Build the devices before the node, not after."
```

---

## Task 2: The payout address

**Files:**
- Create: `payout.go`
- Create: `payout_test.go`
- Modify: `config.go` — add `MiningAddr`, capture `fileMiningAddr`

**Interfaces:**
- Consumes: `config` from `config.go`; `chainParams` (package var, `device.go:21`).
- Produces:
  - `func payoutAddress(effective, fromFile, filePath string) (addr string, needPrompt bool, err error)`
  - `func validatePayout(addr string, params *chaincfg.Params) error`
  - `func checkConfigPerms(path string) error`
  - `func appendMiningAddr(path, addr string) error`
  - `func resolvePayout(cfg *config) (string, error)` — called by `main.go` in
    Task 7, returns the address that Task 3 passes to the node and Task 5
    compares the coinbase against.

- [ ] **Step 1: Add the config fields**

In `config.go`, add to the `config` struct, just above `Benchmark`:

```go
	// Mining options
	MiningAddr string `long:"miningaddr" description:"Address that block rewards are paid to.  Asked for once on first run and saved to the config file"`
	Mond       string `long:"mond" description:"Path to the mond binary, for installations where it does not sit beside monvark"`

	// fileMiningAddr is MiningAddr as it appeared in the config file, captured
	// before the command line is parsed a second time.  After that parse
	// MiningAddr is the effective value and the file's own is unrecoverable,
	// so a disagreement between the two could not otherwise be reported.
	fileMiningAddr string
```

go-flags ignores unexported fields, so `fileMiningAddr` produces no option.

- [ ] **Step 2: Capture the file's value in `loadConfig`**

In `config.go`, between the ini parse and the second command line parse — that
is, immediately before the comment `// Parse command line options again to
ensure they take precedence.` — insert:

```go
	// Capture the config file's own miningaddr before the command line
	// overrides it, so a disagreement between the two can be reported.
	cfg.fileMiningAddr = cfg.MiningAddr
```

- [ ] **Step 3: Write the failing tests**

Create `payout_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monetarium/monetarium-node/chaincfg"
)

func TestPayoutAddress(t *testing.T) {
	const file = "/home/u/.monvark/monvark.conf"

	tests := []struct {
		name       string
		effective  string
		fromFile   string
		wantAddr   string
		wantPrompt bool
		wantErr    bool
	}{{
		name:       "neither source supplies one",
		effective:  "",
		fromFile:   "",
		wantPrompt: true,
	}, {
		name:      "flag only",
		effective: "Vs1",
		fromFile:  "",
		wantAddr:  "Vs1",
	}, {
		name:      "file only",
		effective: "Vs1",
		fromFile:  "Vs1",
		wantAddr:  "Vs1",
	}, {
		name:      "flag agrees with file",
		effective: "Vs1",
		fromFile:  "Vs1",
		wantAddr:  "Vs1",
	}, {
		name:      "flag disagrees with file",
		effective: "Vs2",
		fromFile:  "Vs1",
		wantErr:   true,
	}}

	for _, test := range tests {
		addr, prompt, err := payoutAddress(test.effective, test.fromFile, file)
		if test.wantErr {
			if err == nil {
				t.Errorf("%s: want error, got none", test.name)
				continue
			}
			if !strings.Contains(err.Error(), file) {
				t.Errorf("%s: error does not name the config file: %v",
					test.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", test.name, err)
			continue
		}
		if prompt != test.wantPrompt {
			t.Errorf("%s: prompt = %v, want %v", test.name, prompt,
				test.wantPrompt)
		}
		if addr != test.wantAddr {
			t.Errorf("%s: addr = %q, want %q", test.name, addr, test.wantAddr)
		}
	}
}

func TestValidatePayoutRejectsWrongNetwork(t *testing.T) {
	// A simnet address supplied while mainnet is active must be rejected in
	// the miner's own words rather than surfacing later as the node exiting.
	const simnetAddr = "SsUMGgvWLcixEeHv3GT4TGYyez3s2r5CzkT"

	if err := validatePayout(simnetAddr, chaincfg.SimNetParams()); err != nil {
		t.Fatalf("simnet address rejected on simnet: %v", err)
	}
	if err := validatePayout(simnetAddr, chaincfg.MainNetParams()); err == nil {
		t.Fatal("simnet address accepted on mainnet")
	}
	if err := validatePayout("not-an-address", chaincfg.MainNetParams()); err == nil {
		t.Fatal("garbage accepted as an address")
	}
	if err := validatePayout("", chaincfg.MainNetParams()); err == nil {
		t.Fatal("empty string accepted as an address")
	}
}

func TestCheckConfigPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monvark.conf")
	if err := os.WriteFile(path, []byte("miningaddr=Vs1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkConfigPerms(path); err != nil {
		t.Fatalf("0600 rejected: %v", err)
	}

	// Write access to this file is write access to the payout, so anything
	// wider than the owner is refused.
	if err := os.Chmod(path, 0620); err != nil {
		t.Fatal(err)
	}
	if err := checkConfigPerms(path); err == nil {
		t.Fatal("group-writable config accepted")
	}
	if err := os.Chmod(path, 0602); err != nil {
		t.Fatal(err)
	}
	if err := checkConfigPerms(path); err == nil {
		t.Fatal("world-writable config accepted")
	}

	// A file that does not exist yet is not an error: first run creates it.
	if err := checkConfigPerms(filepath.Join(dir, "absent.conf")); err != nil {
		t.Fatalf("absent config rejected: %v", err)
	}
}

func TestAppendMiningAddr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monvark.conf")

	if err := appendMiningAddr(path, "Vs1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("created with mode %v, want 0600", perm)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "miningaddr=Vs1") {
		t.Fatalf("address not written: %q", got)
	}

	// Appending to a file that already has content preserves it.
	if err := os.WriteFile(path, []byte("debuglevel=debug\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := appendMiningAddr(path, "Vs2"); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "debuglevel=debug") {
		t.Fatalf("existing content lost: %q", got)
	}
	if !strings.Contains(string(got), "miningaddr=Vs2") {
		t.Fatalf("address not appended: %q", got)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test -run 'TestPayoutAddress|TestValidatePayout|TestCheckConfigPerms|TestAppendMiningAddr' -v .`
Expected: FAIL — `undefined: payoutAddress`, `undefined: validatePayout`,
`undefined: checkConfigPerms`, `undefined: appendMiningAddr`.

- [ ] **Step 5: Write `payout.go`**

Create `payout.go`:

```go
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/txscript/stdaddr"
)

// payoutAddress reports which payout address to use.  effective is the value
// after the command line has overridden the config file; fromFile is the config
// file's own value, captured before that override.  needPrompt is true when
// neither source supplied one.
//
// A command line address that disagrees with the file is an error naming the
// file.  The file is never rewritten to match and never silently overwritten:
// that is the install-versus-upgrade distinction, and getting it wrong is the
// bug that ships when a user re-extracts the archive over an existing install.
func payoutAddress(effective, fromFile, filePath string) (string, bool, error) {
	if effective == "" {
		return "", true, nil
	}
	if fromFile != "" && effective != fromFile {
		return "", false, fmt.Errorf("the payout address on the command "+
			"line (%s) disagrees with the one in %s (%s); remove one of "+
			"them, this file is never overwritten", effective, filePath,
			fromFile)
	}
	return effective, false, nil
}

// validatePayout reports whether addr is a valid address on params.  It is
// called before anything is written, so a typo or a testnet address supplied on
// mainnet is reported in the miner's own words rather than surfacing later as
// the node exiting during a progress display.
func validatePayout(addr string, params *chaincfg.Params) error {
	if addr == "" {
		return errors.New("no payout address")
	}
	if _, err := stdaddr.DecodeAddress(addr, params); err != nil {
		return fmt.Errorf("%q is not a valid %s address: %w", addr,
			params.Name, err)
	}
	return nil
}

// checkConfigPerms fails when path is group- or world-writable.  Every field in
// the config file parses back into the config struct, including the payout
// address, so write access to it is write access to the payout.  A file that
// does not exist yet is not an error: first run creates it.
func checkConfigPerms(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		return fmt.Errorf("%s is writable by others (mode %04o); "+
			"run: chmod 600 %s", path, perm, path)
	}
	return nil
}

// appendMiningAddr appends the payout address to path, creating it 0600 if it
// does not exist and preserving whatever is already there.
func appendMiningAddr(path, addr string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "miningaddr=%s\n", addr)
	return err
}

// promptPayout asks for an address on out and reads one from in.
func promptPayout(in io.Reader, out io.Writer) (string, error) {
	fmt.Fprint(out, "\nNo payout address configured.\n"+
		"Block rewards are paid to a Monetarium address you control.  Create\n"+
		"one with monetarium-wallet and paste it below.  The wallet is not\n"+
		"needed while mining and should not be installed on this machine;\n"+
		"monvark never holds keys.\n\nPayout address: ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return trimSpace(line), nil
}

// trimSpace removes surrounding whitespace, including the newline the terminal
// supplies and the carriage return Windows adds to it.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// resolvePayout returns the address block rewards are paid to, asking the user
// for one on first run.  Precedence is the command line, then the config file,
// then the prompt.
func resolvePayout(cfg *config) (string, error) {
	if err := checkConfigPerms(cfg.ConfigFile); err != nil {
		return "", err
	}

	addr, needPrompt, err := payoutAddress(cfg.MiningAddr, cfg.fileMiningAddr,
		cfg.ConfigFile)
	if err != nil {
		return "", err
	}

	if !needPrompt {
		if err := validatePayout(addr, chainParams); err != nil {
			return "", err
		}
		mainLog.Infof("Paying block rewards to %s", addr)
		return addr, nil
	}

	// Only a terminal can answer a question.  Under systemd or Docker there is
	// nobody to ask, so say what to do instead of blocking on a closed stdin.
	if !isTerminal(os.Stdin) {
		return "", fmt.Errorf("no payout address configured and stdin is not "+
			"a terminal; pass --miningaddr, or add miningaddr= to %s",
			cfg.ConfigFile)
	}

	addr, err = promptPayout(os.Stdin, os.Stdout)
	if err != nil {
		return "", err
	}
	// Validate before writing anything.
	if err := validatePayout(addr, chainParams); err != nil {
		return "", err
	}
	if err := appendMiningAddr(cfg.ConfigFile, addr); err != nil {
		return "", err
	}
	mainLog.Infof("Payout address saved to %s", cfg.ConfigFile)
	return addr, nil
}
```

- [ ] **Step 6: Add the terminal check**

`isTerminal` needs no dependency: a character device is enough to distinguish a
terminal from a pipe or a closed stdin. Add to `payout.go`:

```go
// isTerminal reports whether f is a character device, which is enough to tell a
// terminal from the pipe or closed descriptor a service manager supplies.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test -run 'TestPayoutAddress|TestValidatePayout|TestCheckConfigPerms|TestAppendMiningAddr' -v .`
Expected: PASS, all four.

If `TestValidatePayoutRejectsWrongNetwork` fails because the hardcoded simnet
address is not valid for this chain, generate a real one instead of loosening
the test — the point of the test is that a wrong-network address is rejected,
and an address that decodes on both networks would prove nothing. Obtain one
with `monetarium-wallet` on simnet, or from
`chaincfg.SimNetParams()` plus `stdaddr` in a scratch program, and paste it in.

- [ ] **Step 8: Verify the whole suite and the linter**

Run: `./run_tests.sh`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add payout.go payout_test.go config.go
git commit -m "config: Ask for the payout address once, and validate it first."
```

---

## Task 3: The node as a child process

**Files:**
- Create: `node.go`
- Create: `node_test.go`
- Modify: `go.mod`, `go.sum` — `txscript` becomes a direct dependency

**Interfaces:**
- Consumes: `resolvePayout` (Task 2) supplies the address; `config`;
  `chainParams`.
- Produces:
  - `type nodeProc struct` with methods `Stop()` and, in Task 4,
    `WaitSynced(ctx context.Context) error`
  - `func nodeStart(ctx context.Context, cfg *config, addr string) (*nodeProc, error)`
  - `func mondConf(rpcUser, rpcPass, rpcListen, rpcCert, rpcKey string) string`
  - `func checkMondConf(path string) error`
  - `func mondArgs(cfg *config, confPath, appData, addr string, p2pBusy bool) []string`
  - `func mondPath(exeDir, override string) (string, error)`

- [ ] **Step 1: Write the failing tests**

Create `node_test.go`:

```go
package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMondConf(t *testing.T) {
	got := mondConf("u", "p", "127.0.0.1:41287", "/h/rpc.cert", "/h/rpc.key")

	for _, want := range []string{
		"rpcuser=u",
		"rpcpass=p",
		"rpclisten=127.0.0.1:41287",
		"rpccert=/h/rpc.cert",
		"rpckey=/h/rpc.key",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mond.conf missing %q:\n%s", want, got)
		}
	}

	// The two keys that could redirect the payout must never be written to a
	// file.  miningaddr goes on the command line because the node picks
	// uniformly at random among its configured addresses and go-flags appends
	// rather than overrides, so an address in both places pays a random
	// fraction of blocks elsewhere.
	for _, forbidden := range []string{"miningaddr", "generate"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("mond.conf contains %q, which must stay on the command "+
				"line:\n%s", forbidden, got)
		}
	}
}

func TestCheckMondConf(t *testing.T) {
	dir := t.TempDir()

	// A file we wrote ourselves is fine.
	ours := filepath.Join(dir, "ours.conf")
	if err := os.WriteFile(ours, []byte(mondConf("u", "p", "a", "c", "k")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkMondConf(ours); err != nil {
		t.Fatalf("our own mond.conf rejected: %v", err)
	}

	// An absent file is fine: it is about to be written.
	if err := checkMondConf(filepath.Join(dir, "absent.conf")); err != nil {
		t.Fatalf("absent mond.conf rejected: %v", err)
	}

	// A file somebody added a payout-redirecting key to is not.  Overwriting
	// would in fact be safe, since our values win -- but silently repairing it
	// hides exactly the event worth reporting.
	for _, key := range []string{"miningaddr=Vs1", "generate=1"} {
		path := filepath.Join(dir, "tampered.conf")
		body := mondConf("u", "p", "a", "c", "k") + key + "\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkMondConf(path); err == nil {
			t.Errorf("mond.conf containing %q accepted", key)
		}
	}

	if runtime.GOOS != "windows" {
		perm := filepath.Join(dir, "perm.conf")
		if err := os.WriteFile(perm, []byte(mondConf("u", "p", "a", "c", "k")), 0666); err != nil {
			t.Fatal(err)
		}
		if err := checkMondConf(perm); err == nil {
			t.Error("world-writable mond.conf accepted")
		}
	}
}

func TestMondArgs(t *testing.T) {
	cfg := &config{}
	args := mondArgs(cfg, "/h/mond.conf", "/h/node", "Vs1", false)

	want := []string{
		"--configfile=/h/mond.conf",
		"--appdata=/h/node",
		"--miningaddr=Vs1",
	}
	for _, w := range want {
		if !slices.Contains(args, w) {
			t.Errorf("args missing %q: %v", w, args)
		}
	}

	// --generate is never passed: the node's default is off, and a node with
	// CPU mining enabled refuses getwork.
	for _, arg := range args {
		if strings.HasPrefix(arg, "--generate") {
			t.Errorf("args contain %q", arg)
		}
	}

	// The P2P port is left alone when the default one is free, so the node
	// listens as usual and contributes fully to the network.
	for _, arg := range args {
		if strings.HasPrefix(arg, "--listen") {
			t.Errorf("args contain %q when the default P2P port is free", arg)
		}
	}

	// When it is busy -- because another node already holds it -- an ephemeral
	// port is taken instead.  Without this the child dies at startup with
	// "no valid listen address".
	busy := mondArgs(cfg, "/h/mond.conf", "/h/node", "Vs1", true)
	if !slices.Contains(busy, "--listen=:0") {
		t.Errorf("args missing --listen=:0 when the P2P port is busy: %v", busy)
	}

	testnet := mondArgs(&config{TestNet: true}, "/h/mond.conf", "/h/node", "Vs1", false)
	if !slices.Contains(testnet, "--testnet") {
		t.Errorf("args missing --testnet: %v", testnet)
	}
	simnet := mondArgs(&config{SimNet: true}, "/h/mond.conf", "/h/node", "Vs1", false)
	if !slices.Contains(simnet, "--simnet") {
		t.Errorf("args missing --simnet: %v", simnet)
	}
}

func TestMondPath(t *testing.T) {
	dir := t.TempDir()
	name := "mond"
	if runtime.GOOS == "windows" {
		name = "mond.exe"
	}
	beside := filepath.Join(dir, name)
	if err := os.WriteFile(beside, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}

	got, err := mondPath(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != beside {
		t.Errorf("mondPath = %q, want %q", got, beside)
	}

	// An override wins, for installations where the node does not sit beside
	// the miner.
	got, err = mondPath(dir, beside)
	if err != nil {
		t.Fatal(err)
	}
	if got != beside {
		t.Errorf("override ignored: %q", got)
	}

	// The error names the path searched, so the user knows where to put it.
	empty := t.TempDir()
	_, err = mondPath(empty, "")
	if err == nil {
		t.Fatal("missing node binary accepted")
	}
	if !strings.Contains(err.Error(), empty) {
		t.Errorf("error does not name the path searched: %v", err)
	}
}

func TestPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if portFree(net.JoinHostPort("127.0.0.1", port)) {
		t.Error("a port we are holding reported free")
	}

	ln.Close()
	if !portFree(net.JoinHostPort("127.0.0.1", port)) {
		t.Error("a released port reported busy")
	}
}

func TestFreePort(t *testing.T) {
	port, err := freePort("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("freePort returned %d", port)
	}
	// The port it hands back must actually be bindable.
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatalf("port %d not bindable: %v", port, err)
	}
	ln.Close()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -run 'TestMond|TestPortFree|TestFreePort' -v .`
Expected: FAIL — `undefined: mondConf`, `undefined: checkMondConf`,
`undefined: mondArgs`, `undefined: mondPath`, `undefined: portFree`,
`undefined: freePort`, `undefined: itoa`.

- [ ] **Step 3: Write `node.go`**

Create `node.go`:

```go
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/monetarium/monetarium-node/rpcclient"
)

// nodeProc is the node monvark started and owns.
type nodeProc struct {
	cmd *exec.Cmd
	rpc *rpcclient.Client
}

// itoa is strconv.Itoa under a shorter name, used where a port becomes part of
// an address.
func itoa(i int) string { return strconv.Itoa(i) }

// freePort binds port 0 on host, reads the port the OS assigned and releases
// it.  There is a small window between the release and the node's own bind in
// which another process could take it; it is local, brief, and the standard way
// to do this.  The node failing to bind is a clear startup error, not a silent
// misbehaviour.
func freePort(host string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// portFree reports whether hostport can be bound right now.
func portFree(hostport string) bool {
	ln, err := net.Listen("tcp", hostport)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// randomCredential returns 32 bytes of hex from crypto/rand.
func randomCredential() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// mondConf renders the node's configuration.  It carries the credentials and
// nothing else: keeping rpcpass out of the command line keeps it out of `ps`,
// while the payout address stays on the command line because a miningaddr in a
// file would be appended to, not overridden by, the one we pass.
func mondConf(rpcUser, rpcPass, rpcListen, rpcCert, rpcKey string) string {
	return fmt.Sprintf(""+
		"; Generated by monvark.  Rewritten on every start; edits are lost.\n"+
		"; The payout address is deliberately not here -- it is passed on the\n"+
		"; node's command line, because the node picks at random among its\n"+
		"; configured mining addresses and a second one would take a share.\n"+
		"rpcuser=%s\n"+
		"rpcpass=%s\n"+
		"rpclisten=%s\n"+
		"rpccert=%s\n"+
		"rpckey=%s\n",
		rpcUser, rpcPass, rpcListen, rpcCert, rpcKey)
}

// checkMondConf validates an existing node configuration before it is replaced.
// Overwriting would be safe, since our values win, but silently repairing a
// file somebody edited hides the event worth reporting.
func checkMondConf(path string) error {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	if info, err := os.Stat(path); err == nil {
		if perm := info.Mode().Perm(); perm&0077 != 0 && runtime.GOOS != "windows" {
			return fmt.Errorf("%s is writable by others (mode %04o); "+
				"run: chmod 600 %s", path, perm, path)
		}
	}

	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		switch strings.TrimSpace(key) {
		case "miningaddr", "generate":
			return fmt.Errorf("%s contains %q, which monvark never writes; "+
				"it would redirect the block reward.  Delete the file and "+
				"restart, or investigate who added it", path, key)
		}
	}
	return nil
}

// mondArgs builds the node's command line.  --generate is never passed: the
// node's default is off, and a node with CPU mining enabled refuses getwork.
func mondArgs(cfg *config, confPath, appData, addr string, p2pBusy bool) []string {
	args := []string{
		"--configfile=" + confPath,
		"--appdata=" + appData,
		"--miningaddr=" + addr,
	}
	if p2pBusy {
		// Another node already holds the default P2P port.  Without this the
		// child dies at startup: the node skips a P2P address it cannot bind
		// but fails outright when none succeeded.  On an ephemeral port it
		// still dials out, syncs, validates and relays -- it just cannot
		// accept inbound peers, which is already true of any rig behind NAT.
		args = append(args, "--listen=:0")
	}
	switch {
	case cfg.TestNet:
		args = append(args, "--testnet")
	case cfg.SimNet:
		args = append(args, "--simnet")
	}
	return args
}

// mondPath resolves the node binary.  It sits beside monvark in the archive;
// override covers installations where it does not.
func mondPath(exeDir, override string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("--mond %s: %w", override, err)
		}
		return override, nil
	}

	name := "mond"
	if runtime.GOOS == "windows" {
		name = "mond.exe"
	}
	path := filepath.Join(exeDir, name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("the node binary %q was not found in %s; it "+
			"ships in the same archive as monvark, or pass --mond with its "+
			"path", name, exeDir)
	}
	return path, nil
}

// nodeStart configures and starts the node, and returns it with an RPC client
// connected to it.
func nodeStart(ctx context.Context, cfg *config, addr string) (*nodeProc, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	bin, err := mondPath(filepath.Dir(exe), cfg.Mond)
	if err != nil {
		return nil, err
	}

	confPath := filepath.Join(minerHomeDir, "mond.conf")
	if err := checkMondConf(confPath); err != nil {
		return nil, err
	}

	rpcPort, err := freePort("127.0.0.1")
	if err != nil {
		return nil, err
	}
	rpcListen := net.JoinHostPort("127.0.0.1", itoa(rpcPort))

	rpcUser, err := randomCredential()
	if err != nil {
		return nil, err
	}
	rpcPass, err := randomCredential()
	if err != nil {
		return nil, err
	}

	certPath := filepath.Join(minerHomeDir, "rpc.cert")
	keyPath := filepath.Join(minerHomeDir, "rpc.key")
	conf := mondConf(rpcUser, rpcPass, rpcListen, certPath, keyPath)
	if err := os.WriteFile(confPath, []byte(conf), 0600); err != nil {
		return nil, err
	}

	// The default P2P port belongs to whoever took it first.  We coexist with
	// an existing node rather than fighting it for the port or killing it.
	p2pDefault := net.JoinHostPort("", chainParams.DefaultPort)
	p2pBusy := !portFree(p2pDefault)
	if p2pBusy {
		mainLog.Warnf("P2P port %s is already in use, most likely by another "+
			"node; starting ours on an ephemeral port.  It will sync and "+
			"relay normally but cannot accept inbound peers.",
			chainParams.DefaultPort)
	}

	appData := filepath.Join(minerHomeDir, "node")
	args := mondArgs(cfg, confPath, appData, addr, p2pBusy)

	cmd := exec.CommandContext(ctx, bin, args...)
	// Inherit stderr so startup failures are visible.  The node's own logs go
	// to appData/logs as normal.
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start %s: %w", bin, err)
	}
	mainLog.Infof("Started the node on %s", rpcListen)

	// The miner connects to the node monvark just started, so tell the rest of
	// the program where it is and how to authenticate to it.
	cfg.RPCServer = rpcListen
	cfg.RPCCert = certPath
	cfg.RPCUser = rpcUser
	cfg.RPCPassword = rpcPass

	n := &nodeProc{cmd: cmd}
	n.rpc, err = n.connect(ctx, cfg)
	if err != nil {
		n.Stop()
		return nil, err
	}
	return n, nil
}

// connect waits for the node's RPC server to come up.  It retries because the
// node needs a moment to start and to generate its certificate, which does not
// exist at all on a first run.
func (n *nodeProc) connect(ctx context.Context, cfg *config) (*rpcclient.Client, error) {
	const timeout = 30 * time.Second

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if n.cmd.ProcessState != nil {
			return nil, fmt.Errorf("the node exited during startup: %v",
				n.cmd.ProcessState)
		}

		certs, err := os.ReadFile(cfg.RPCCert)
		if err != nil {
			lastErr = err
		} else {
			client, err := rpcclient.New(&rpcclient.ConnConfig{
				Host:         cfg.RPCServer,
				User:         cfg.RPCUser,
				Pass:         cfg.RPCPassword,
				Certificates: certs,
				HTTPPostMode: true,
			}, nil)
			if err == nil {
				if _, err = client.GetBlockChainInfo(ctx); err == nil {
					return client, nil
				}
				client.Shutdown()
			}
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("the node's RPC server did not come up within %v: %w",
		timeout, lastErr)
}

// Stop shuts the node down.  It uses the node's own stop RPC rather than a
// signal: os.Interrupt cannot be delivered through Process.Signal on Windows,
// and the Kill that would remain there risks the node's database.  This is one
// code path on every platform.
func (n *nodeProc) Stop() {
	if n.rpc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := n.rpc.RawRequest(ctx, "stop", nil); err != nil {
			mainLog.Warnf("Failed to ask the node to stop: %v", err)
		}
		cancel()
		n.rpc.Shutdown()
	}
	if n.cmd == nil || n.cmd.Process == nil {
		return
	}

	done := make(chan struct{})
	go func() {
		n.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		mainLog.Info("The node shut down.")
	case <-time.After(30 * time.Second):
		mainLog.Warn("The node did not shut down in time; killing it.")
		n.cmd.Process.Kill()
		<-done
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -run 'TestMond|TestPortFree|TestFreePort' -v .`
Expected: PASS, all six.

- [ ] **Step 5: Make `txscript` a direct dependency**

`stdaddr` lives in the `txscript` module, which `go.mod` currently lists as
indirect. Run: `go mod tidy`
Expected: `github.com/monetarium/monetarium-node/txscript v1.3.10` moves out of
the indirect block. Check with: `grep -n txscript go.mod`

- [ ] **Step 6: Run the whole suite and the linter**

Run: `./run_tests.sh`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add node.go node_test.go go.mod go.sum
git commit -m "node: Configure, start and supervise the node monvark owns."
```

---

## Task 4: The sync gate

Gate on `!InitialBlockDownload`, never on `blocks >= headers`: on a clean machine
both are 0 before any header arrives, which reads as "synced" and mines at
height 1. The progress line carries the peer count because
`InitialBlockDownload` is `!chain.IsCurrent()` and `IsCurrent` never becomes true
without peers, so a firewalled machine would otherwise sit at 0.0% forever with
no explanation.

**Files:**
- Modify: `node.go` — add `synced`, `syncLine`, `WaitSynced`
- Modify: `node_test.go` — add the predicate tests

**Interfaces:**
- Consumes: `nodeProc` and its `rpc` field (Task 3).
- Produces: `func (n *nodeProc) WaitSynced(ctx context.Context) error`, called by
  `main.go` in Task 7.

- [ ] **Step 1: Write the failing tests**

Append to `node_test.go`:

```go
func TestSynced(t *testing.T) {
	// The gate is the boolean, never blocks >= headers.  On a clean machine
	// both counts are 0 before any header arrives, which would read as
	// "synced" and mine at height 1.
	fresh := &chainjson.GetBlockChainInfoResult{
		Blocks:               0,
		Headers:              0,
		InitialBlockDownload: true,
	}
	if synced(fresh) {
		t.Error("a node with no headers yet reported synced")
	}

	// Mid-sync, with the counts equal because headers arrive in batches.
	midway := &chainjson.GetBlockChainInfoResult{
		Blocks:               1000,
		Headers:              1000,
		InitialBlockDownload: true,
	}
	if synced(midway) {
		t.Error("a node still in initial block download reported synced")
	}

	done := &chainjson.GetBlockChainInfoResult{
		Blocks:               31023,
		Headers:              31023,
		InitialBlockDownload: false,
	}
	if !synced(done) {
		t.Error("a caught-up node reported not synced")
	}
}

func TestSyncLine(t *testing.T) {
	info := &chainjson.GetBlockChainInfoResult{
		Blocks:               10612,
		Headers:              31023,
		VerificationProgress: 0.342,
	}
	got := syncLine(info, 6)

	for _, want := range []string{"34.2", "10612", "31023", "6"} {
		if !strings.Contains(got, want) {
			t.Errorf("sync line missing %q: %q", want, got)
		}
	}

	// Zero peers is the case the line exists for: without it a firewalled
	// machine sits at 0.0%% forever with no explanation.
	if !strings.Contains(syncLine(info, 0), "0") {
		t.Errorf("sync line omits the peer count: %q", syncLine(info, 0))
	}
}
```

Add to `node_test.go`'s imports:

```go
	chainjson "github.com/monetarium/monetarium-node/rpc/jsonrpc/types"
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -run 'TestSynced|TestSyncLine' -v .`
Expected: FAIL — `undefined: synced`, `undefined: syncLine`.

- [ ] **Step 3: Implement the gate**

Add to `node.go`:

```go
// synced reports whether the chain is far enough along to mine on.
//
// The test is the boolean, never blocks >= headers: on a clean machine both are
// 0 before any header arrives, which reads as synced and mines at height 1.
func synced(info *chainjson.GetBlockChainInfoResult) bool {
	return !info.InitialBlockDownload
}

// syncLine renders one line of sync progress.
//
// The peer count is not decoration.  InitialBlockDownload is !chain.IsCurrent(),
// and IsCurrent never becomes true without peers, so a machine that cannot
// reach the network would otherwise sit at 0.0% forever with nothing to explain
// why.
func syncLine(info *chainjson.GetBlockChainInfoResult, peers int64) string {
	return fmt.Sprintf("Syncing %5.1f%%  %d/%d blocks  %d peers",
		info.VerificationProgress*100, info.Blocks, info.Headers, peers)
}

// WaitSynced blocks until the node has caught up with the network.  Mining does
// not start before this returns.
func (n *nodeProc) WaitSynced(ctx context.Context) error {
	const poll = 2 * time.Second

	for {
		if n.cmd.ProcessState != nil {
			return fmt.Errorf("the node exited while syncing: %v",
				n.cmd.ProcessState)
		}

		info, err := n.rpc.GetBlockChainInfo(ctx)
		if err != nil {
			return fmt.Errorf("unable to read the node's chain state: %w", err)
		}
		if synced(info) {
			mainLog.Infof("Synced at height %d.", info.Blocks)
			return nil
		}

		peers, err := n.rpc.GetConnectionCount(ctx)
		if err != nil {
			peers = 0
		}
		mainLog.Info(syncLine(info, peers))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
```

Add to `node.go`'s imports:

```go
	chainjson "github.com/monetarium/monetarium-node/rpc/jsonrpc/types"
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -run 'TestSynced|TestSyncLine' -v .`
Expected: PASS.

- [ ] **Step 5: Run the whole suite and the linter**

Run: `./run_tests.sh`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add node.go node_test.go
git commit -m "node: Refuse to mine before the chain has caught up."
```

---

## Task 5: Prove on-chain that the coinbase paid the user

This is the only check in the system that covers where the money goes rather than
whether the hash is right. It is evidence and a bug detector, not a defence
against a substituted node — a hostile `mond` lies on every RPC it serves, and
the archive's SHA256 is what covers that.

**Files:**
- Modify: `miner.go` — `coinbasePays`, the assertion in `workSubmitThread`
- Create: `miner_test.go`

**Interfaces:**
- Consumes: the address from `resolvePayout` (Task 2); `Miner.rpc`.
- Produces:
  - `func coinbasePays(blk *chainjson.GetBlockVerboseResult, wantScript string) bool`
  - `Miner.payoutAddr string` and `Miner.payoutScript string` fields, read by
    Task 7's status output.

- [ ] **Step 1: Write the failing test**

Create `miner_test.go`:

```go
package main

import (
	"testing"

	chainjson "github.com/monetarium/monetarium-node/rpc/jsonrpc/types"
)

// block builds a verbose block result whose coinbase pays the given scripts.
func block(scripts ...string) *chainjson.GetBlockVerboseResult {
	var vouts []chainjson.Vout
	for i, s := range scripts {
		vouts = append(vouts, chainjson.Vout{
			N:            uint32(i),
			ScriptPubKey: chainjson.ScriptPubKeyResult{Hex: s},
		})
	}
	return &chainjson.GetBlockVerboseResult{
		RawTx: []chainjson.TxRawResult{{Vout: vouts}},
	}
}

func TestCoinbasePays(t *testing.T) {
	const ours = "76a914aabbccddeeff00112233445566778899aabbccdd88ac"
	const theirs = "76a914ffffffffffffffffffffffffffffffffffffffff88ac"

	tests := []struct {
		name string
		blk  *chainjson.GetBlockVerboseResult
		want bool
	}{{
		name: "single output, ours",
		blk:  block(ours),
		want: true,
	}, {
		name: "single output, somebody else's",
		blk:  block(theirs),
		want: false,
	}, {
		// A coinbase carries the treasury and other outputs alongside the
		// miner's, so ours being one of several is the normal case.
		name: "several outputs, one ours",
		blk:  block(theirs, ours, theirs),
		want: true,
	}, {
		name: "several outputs, none ours",
		blk:  block(theirs, theirs),
		want: false,
	}, {
		name: "no transactions at all",
		blk:  &chainjson.GetBlockVerboseResult{},
		want: false,
	}, {
		name: "coinbase with no outputs",
		blk:  block(),
		want: false,
	}}

	for _, test := range tests {
		if got := coinbasePays(test.blk, ours); got != test.want {
			t.Errorf("%s: coinbasePays = %v, want %v", test.name, got,
				test.want)
		}
	}
}

func TestCoinbasePaysIsCaseInsensitive(t *testing.T) {
	const lower = "76a914aabbccddeeff00112233445566778899aabbccdd88ac"
	const upper = "76A914AABBCCDDEEFF00112233445566778899AABBCCDD88AC"

	if !coinbasePays(block(upper), lower) {
		t.Error("hex case difference reported as a mismatch")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run TestCoinbasePays -v .`
Expected: FAIL — `undefined: coinbasePays`.

- [ ] **Step 3: Implement `coinbasePays`**

Add to `miner.go`:

```go
// coinbasePays reports whether the coinbase of blk pays wantScript.
//
// The comparison is against the raw payment script rather than the address
// string that also appears in the result: comparing scripts is the exact check
// the design wanted, and it survives address-encoding variations that string
// matching would not.  A coinbase carries the treasury and other outputs
// alongside the miner's, so ours being one of several is the normal case.
func coinbasePays(blk *chainjson.GetBlockVerboseResult, wantScript string) bool {
	if len(blk.RawTx) == 0 {
		return false
	}
	for _, out := range blk.RawTx[0].Vout {
		if strings.EqualFold(out.ScriptPubKey.Hex, wantScript) {
			return true
		}
	}
	return false
}
```

Add `"strings"` and
`chainjson "github.com/monetarium/monetarium-node/rpc/jsonrpc/types"` to
`miner.go`'s imports.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -run TestCoinbasePays -v .`
Expected: PASS.

- [ ] **Step 5: Add the payout fields to `Miner`**

In `miner.go`, add to the `Miner` struct:

```go
	// payoutAddr is the address block rewards are paid to, and payoutScript
	// its payment script.  The assertion below compares the coinbase against
	// the script; the address is what the user sees.
	payoutAddr   string
	payoutScript string
	payeeChecked bool
```

Change `NewMiner`'s signature to take the address and fill them in. Replace the
opening of `NewMiner` with:

```go
func NewMiner(ctx context.Context, devices []*Device, workDone chan []byte, payoutAddr string) (*Miner, error) {
	var m *Miner
	var err error
	if cfg.Benchmark {
		m = &Miner{devices: devices}
	} else {
		m, err = newSoloMiner(ctx, devices)
	}
	if err != nil {
		return nil, err
	}

	if payoutAddr != "" {
		addr, err := stdaddr.DecodeAddress(payoutAddr, chainParams)
		if err != nil {
			return nil, err
		}
		_, script := addr.PaymentScript()
		m.payoutAddr = payoutAddr
		m.payoutScript = hex.EncodeToString(script)
	}
```

Add `"github.com/monetarium/monetarium-node/txscript/stdaddr"` to `miner.go`'s
imports. `encoding/hex` is already imported.

- [ ] **Step 6: Assert on the first accepted block**

In `miner.go`, inside `workSubmitThread`, replace the success line:

```go
			atomic.AddUint64(&m.validShares, 1)
			minrLog.Infof("Submitted work successfully: block hash %v",
				chainhash.Hash(blake256.Sum256(data[:180])))
```

with:

```go
			atomic.AddUint64(&m.validShares, 1)
			hash := chainhash.Hash(blake256.Sum256(data[:180]))
			minrLog.Infof("Submitted work successfully: block hash %v", hash)

			if err := m.checkPayee(ctx, &hash); err != nil {
				minrLog.Criticalf("%v", err)
				return
			}
```

Then add the method:

```go
// checkPayee reads back a block this miner submitted and asserts its coinbase
// pays the user.  It runs once, on the first accepted block: this is the only
// point in the system that covers where the money goes rather than whether the
// hash is right.
//
// It is evidence and a bug detector, not a defence against a substituted node
// binary -- a hostile node lies on every RPC it serves.  The archive's SHA256
// is what covers that.
func (m *Miner) checkPayee(ctx context.Context, hash *chainhash.Hash) error {
	if m.payeeChecked || m.payoutScript == "" {
		return nil
	}

	blk, err := m.rpc.GetBlockVerbose(ctx, hash, true)
	if err != nil {
		// A block we cannot read back is not evidence of theft, so warn rather
		// than stopping, and try again on the next one.
		minrLog.Warnf("Unable to read back block %v to verify the payee: %v",
			hash, err)
		return nil
	}

	if !coinbasePays(blk, m.payoutScript) {
		return fmt.Errorf("block %v does not pay %s -- refusing to mine "+
			"further.  The node monvark started was given this address on "+
			"its own command line, so this should be impossible; check that "+
			"the mond binary beside monvark is the one from the release "+
			"archive", hash, m.payoutAddr)
	}

	m.payeeChecked = true
	minrLog.Infof("Coinbase verified: block %d pays %s", blk.Height,
		m.payoutAddr)
	return nil
}
```

- [ ] **Step 7: Update the `NewMiner` call**

In `main.go`, change:

```go
	m, err := NewMiner(ctx, devices, workDone)
```

to:

```go
	m, err := NewMiner(ctx, devices, workDone, payoutAddr)
```

`payoutAddr` is produced by Task 7's wiring; until then, pass `""` so the tree
builds, and Task 7 replaces it. Add this comment where you pass `""`:

```go
	// payoutAddr is wired in below once resolvePayout runs.
```

- [ ] **Step 8: Run the whole suite and the linter**

Run: `./run_tests.sh`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add miner.go miner_test.go main.go
git commit -m "miner: Prove on-chain that the coinbase paid the user."
```

---

## Task 6: Work expiry

`miner.go` calls `GetWork` once at construction; thereafter work arrives only
through the pushed `OnWork` callback. That push path carries neither of the
guards `handleGetWork` has, `device.go` keeps the current work forever if none
arrives, and nothing consults `TimeReceived`. A miner whose websocket dropped
grinds a dead header at full reported hashrate.

The devices are never stopped — the poll's own result already separates a broken
push path from a quiet chain, and stopping is not buildable without new device
state, since `SetWork` is the only way into a device and always arms it. A
repeatedly failing `GetWork` means an unhealthy node, and monvark's policy for an
unhealthy node is to exit.

**Files:**
- Modify: `miner.go` — the expiry ticker
- Modify: `miner_test.go` — the classification tests

**Interfaces:**
- Consumes: `Miner.rpc`, `onSoloWork`, `work.Work.TimeReceived`.
- Produces: `func workStale(received uint32, now time.Time, bound time.Duration) bool`
  and `Miner.expiryThread(ctx context.Context)`.

- [ ] **Step 1: Write the failing test**

Append to `miner_test.go`:

```go
import (
	"time"
)

func TestWorkStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	bound := 15 * time.Minute

	fresh := uint32(now.Add(-1 * time.Minute).Unix())
	if workStale(fresh, now, bound) {
		t.Error("work one minute old reported stale")
	}

	// The bound is three times the five-minute block target, so a healthy
	// quiet chain trips it on roughly 5% of gaps and each trip is harmless.
	borderline := uint32(now.Add(-14 * time.Minute).Unix())
	if workStale(borderline, now, bound) {
		t.Error("work inside the bound reported stale")
	}

	old := uint32(now.Add(-16 * time.Minute).Unix())
	if !workStale(old, now, bound) {
		t.Error("work past the bound not reported stale")
	}

	// Work that has never been received is not stale: there is nothing to
	// have gone stale yet, and benchmark mode never sets it.
	if workStale(0, now, bound) {
		t.Error("unset work reported stale")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run TestWorkStale -v .`
Expected: FAIL — `undefined: workStale`.

- [ ] **Step 3: Implement the expiry**

Add to `miner.go`:

```go
// workExpiry is how long work may go unrefreshed before the guarded GetWork
// path is polled.
//
// The node regenerates a template 30 seconds after new transactions arrive, but
// on a chain with an empty mempool there is no regen and therefore no push, so
// work arrives on new blocks alone -- at a five minute target, Poisson
// distributed.  Fifteen minutes is three times that, so it fires on roughly 5%
// of healthy gaps and each firing is one cheap RPC call.
const workExpiry = 15 * time.Minute

// workStale reports whether work received at the given unix time is older than
// bound.  Work that was never received is not stale: benchmark mode never sets
// it, and there is nothing yet to have gone stale.
func workStale(received uint32, now time.Time, bound time.Duration) bool {
	if received == 0 {
		return false
	}
	return now.Sub(time.Unix(int64(received), 0)) > bound
}

// expiryThread re-issues GetWork when no pushed work has arrived within the
// bound.  It never stops the devices: the poll's own result already separates a
// broken push path from a quiet chain, so a stop would convey nothing while
// costing real hashrate.  A GetWork that keeps failing means an unhealthy node,
// which is handled by exiting rather than by inventing a device pause.
func (m *Miner) expiryThread(ctx context.Context) {
	defer m.wg.Done()

	const maxFailures = 3

	t := time.NewTicker(time.Minute)
	defer t.Stop()

	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		m.workMtx.Lock()
		current := m.currentWork
		m.workMtx.Unlock()
		if current == nil || !workStale(current.TimeReceived, time.Now(), workExpiry) {
			continue
		}

		result, err := m.rpc.GetWork(ctx)
		if err != nil {
			failures++
			minrLog.Warnf("No work for over %v and GetWork failed (%d of %d "+
				"before giving up): %v", workExpiry, failures, maxFailures, err)
			if failures >= maxFailures {
				minrLog.Criticalf("The node has not served work for %v; "+
					"shutting down", time.Duration(maxFailures)*workExpiry)
				m.shutdown()
			}
			continue
		}
		failures = 0

		data, err := hex.DecodeString(result.Data)
		if err != nil {
			minrLog.Errorf("Unable to decode work data: %v", err)
			continue
		}
		target, err := hex.DecodeString(result.Target)
		if err != nil {
			minrLog.Errorf("Unable to decode work target: %v", err)
			continue
		}

		if bytes.Equal(data, current.Data[:len(data)]) {
			minrLog.Debugf("No new work for %v; the chain is quiet and the "+
				"node agrees", workExpiry)
		} else {
			minrLog.Warnf("No pushed work for %v; the push path had stalled "+
				"and polling recovered it", workExpiry)
		}
		onSoloWork(ctx, data, target, "expiry", m.devices)
	}
}
```

Add `"bytes"` to `miner.go`'s imports.

- [ ] **Step 4: Track the current work**

`onSoloWork` is a free function that fans work out to the devices; the expiry
thread needs to know what the newest work was. Add to the `Miner` struct:

```go
	workMtx     sync.Mutex
	currentWork *work.Work
```

Change `onSoloWork` to take the miner and record it. Replace its signature and
the end of its body:

```go
func onSoloWork(ctx context.Context, data, target []byte, reason string, m *Miner) {
```

...and at the end, before the device loop:

```go
	m.workMtx.Lock()
	m.currentWork = w
	m.workMtx.Unlock()

	for _, d := range m.devices {
		d.SetWork(ctx, w)
	}
```

Update the three call sites — the `OnWork` handler in `newSoloMiner`, the
initial `GetWork` in `NewMiner`, and the expiry thread — to pass the miner.
`newSoloMiner` builds its notification handlers before the `Miner` exists, so
build the `Miner` first and assign `m.rpc` afterwards:

```go
func newSoloMiner(ctx context.Context, devices []*Device) (*Miner, error) {
	m := &Miner{devices: devices}

	ntfnHandlers := rpcclient.NotificationHandlers{
		OnBlockConnected: func(blockHeader []byte, transactions [][]byte) {
			minrLog.Infof("Block connected: %x (%d transactions)", blockHeader,
				len(transactions))
		},
		OnBlockDisconnected: func(blockHeader []byte) {
			minrLog.Infof("Block disconnected: %x", blockHeader)
		},
		OnWork: func(data, target []byte, reason string) {
			onSoloWork(ctx, data, target, reason, m)
		},
	}
```

...then the existing certificate read and `rpcclient.New` call, then:

```go
	m.rpc = rpc
	return m, nil
```

- [ ] **Step 5: Add the shutdown hook**

The expiry thread needs a way to end the process. Add to the `Miner` struct:

```go
	// cancel stops the miner's context, which is how a fatal condition
	// discovered by a background thread ends the run.
	cancel context.CancelFunc
```

Add the method:

```go
// shutdown ends the run from a background thread.
func (m *Miner) shutdown() {
	if m.cancel != nil {
		m.cancel()
	}
}
```

In `Run`, derive the cancellable context and start the thread:

```go
func (m *Miner) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.cancel = cancel

	m.wg.Add(len(m.devices))
```

...and after the `workSubmitThread` start, add:

```go
	if !cfg.Benchmark {
		m.wg.Add(1)
		go m.expiryThread(ctx)
	}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test -run TestWorkStale -v .`
Expected: PASS.

- [ ] **Step 7: Run the whole suite and the linter**

Run: `./run_tests.sh`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add miner.go miner_test.go
git commit -m "miner: Poll the guarded work path when pushed work goes stale."
```

---

## Task 7: Wire the startup sequence, trim the flags, show the address

**Files:**
- Modify: `main.go` — the full sequence
- Modify: `config.go` — delete the proxy flags, demote the RPC flags
- Modify: `miner.go` — drop the proxy fields from `ConnConfig`
- Modify: `monitor.go` — the payout address in the status JSON
- Modify: `sample-monvark.conf`

**Interfaces:**
- Consumes: `resolvePayout` (Task 2), `nodeStart` and `Stop` (Task 3),
  `WaitSynced` (Task 4), `NewMiner(ctx, devices, workDone, payoutAddr)`
  (Task 5).
- Produces: the finished startup sequence; `MinerStatus.PayoutAddress`.

- [ ] **Step 1: Demote the RPC flags and delete the proxy flags**

In `config.go`, replace the RPC connection block:

```go
	// RPC connection options
	RPCUser     string `short:"u" long:"rpcuser" description:"RPC username"`
	RPCPassword string `short:"P" long:"rpcpass" default-mask:"-" description:"RPC password"`
	RPCServer   string `short:"s" long:"rpcserver" description:"RPC server to connect to"`
	RPCCert     string `short:"c" long:"rpccert" description:"RPC server certificate chain for validation"`
	Proxy       string `long:"proxy" description:"Connect via SOCKS5 proxy (eg. 127.0.0.1:9050)"`
	ProxyUser   string `long:"proxyuser" description:"Username for proxy server"`
	ProxyPass   string `long:"proxypass" default-mask:"-" description:"Password for proxy server"`
```

with:

```go
	// Connection details for the node monvark starts.  These are not flags:
	// there is no mode for attaching to a node we did not configure, because a
	// getwork template pays the miningaddr of whichever node served it.
	// nodeStart fills them in with the port and credentials it chose.
	RPCUser     string
	RPCPassword string
	RPCServer   string
	RPCCert     string
```

- [ ] **Step 2: Remove the defaults that no longer apply**

In `config.go`, delete these from the `var` block:

```go
	nodeHomeDir           = dcrutil.AppDataDir("monetarium", false)
	defaultRPCServer      = "localhost"
	defaultRPCCertFile    = filepath.Join(nodeHomeDir, "rpc.cert")
	defaultRPCPortMainNet = "9509"
	defaultRPCPortTestNet = "19509"
	defaultRPCPortSimNet  = "19956"
```

In `loadConfig`, delete `RPCServer` and `RPCCert` from the default struct
literal, leaving:

```go
	cfg := config{
		ConfigFile: defaultConfigFile,
		DebugLevel: defaultLogLevel,
	}
```

Delete the RPC path and port handling near the end of `loadConfig`:

```go
	// Handle environment variable expansion in the RPC certificate path.
	cfg.RPCCert = cleanAndExpandPath(cfg.RPCCert)

	var defaultRPCPort string
	switch {
	case cfg.TestNet:
		defaultRPCPort = defaultRPCPortTestNet
		chainParams = chaincfg.TestNet3Params()
	case cfg.SimNet:
		defaultRPCPort = defaultRPCPortSimNet
		chainParams = chaincfg.SimNetParams()
	default:
		defaultRPCPort = defaultRPCPortMainNet
	}

	// Add default port to RPC server based on --testnet flag
	// if needed.
	cfg.RPCServer = normalizeAddress(cfg.RPCServer, defaultRPCPort)
```

and replace it with the network selection alone:

```go
	switch {
	case cfg.TestNet:
		chainParams = chaincfg.TestNet3Params()
	case cfg.SimNet:
		chainParams = chaincfg.SimNetParams()
	}
```

`normalizeAddress` is still used by `--apilisten`, so it stays.
`cleanAndExpandPath` may become unused; if `golangci-lint` reports it as dead,
delete it too.

- [ ] **Step 3: Drop the proxy fields from the RPC connection**

In `miner.go`, in `newSoloMiner`'s `rpcclient.ConnConfig`, delete:

```go
		Proxy:        cfg.Proxy,
		ProxyUser:    cfg.ProxyUser,
		ProxyPass:    cfg.ProxyPass,
```

- [ ] **Step 4: Write the startup sequence**

In `main.go`, replace everything between the device construction (Task 1,
Step 3) and the `NewMiner` call with:

```go
	// Benchmark mode needs no node, no address and no chain: it is the only
	// path that exercises the mining loop on its own.
	var payoutAddr string
	if !cfg.Benchmark {
		payoutAddr, err = resolvePayout(cfg)
		if err != nil {
			mainLog.Criticalf("%v", err)
			return err
		}

		node, err := nodeStart(ctx, cfg, payoutAddr)
		if err != nil {
			mainLog.Criticalf("Unable to start the node: %v", err)
			return err
		}
		defer node.Stop()

		if err := node.WaitSynced(ctx); err != nil {
			mainLog.Criticalf("%v", err)
			return err
		}
	}
```

This must sit **after** the `signal.NotifyContext` block, because `nodeStart`
takes the context that a Ctrl+C cancels. Move the device construction and this
block so the order in `monvarkMain` reads:

1. `loadConfig`
2. version line
3. profiling and CPU profile setup
4. `signal.NotifyContext` and the shutdown goroutine
5. `newDevices`
6. the block above
7. `NewMiner(ctx, devices, workDone, payoutAddr)`
8. `RunMonitor`, `m.Run(ctx)`

- [ ] **Step 5: Show the payout address**

In `monitor.go`, add to `MinerStatus`:

```go
	PayoutAddress string `json:"payoutAddress,omitempty"`
```

and in `getMinerStatus`, inside the `if !cfg.Benchmark` block:

```go
		ms.PayoutAddress = m.payoutAddr
```

In `miner.go`, in `printStatsThread`, change the global stats line:

```go
		if !cfg.Benchmark {
			valid, rejected, total := m.Status()
			minrLog.Infof("Global stats: Accepted: %v, Rejected: %v, "+
				"Total: %v, Paying: %v", valid, rejected, total, m.payoutAddr)
		}
```

`CoinbaseMaturity` is 256, so at the measured block interval the first coin is
not spendable for the better part of a day; without the address on screen the
user has no way to confirm they are being paid.

- [ ] **Step 6: Update the sample config**

In `sample-monvark.conf`, delete any `rpcuser`, `rpcpass`, `rpcserver`,
`rpccert`, `proxy`, `proxyuser` and `proxypass` entries, and add:

```ini
; The address block rewards are paid to.  monvark asks for this once on first
; run and writes it here; you can also set it yourself before the first run.
; miningaddr=

; Path to the mond binary, if it does not sit beside monvark.
; mond=
```

- [ ] **Step 7: Verify the flag surface**

Run: `go build -o /tmp/monvark-t7 . && /tmp/monvark-t7 --help 2>&1 | grep -E 'miningaddr|mond|proxy|rpcserver|rpcuser|rpcpass|rpccert'`
Expected: `--miningaddr` and `--mond` are listed; none of `proxy`, `rpcserver`,
`rpcuser`, `rpcpass` or `rpccert` appears.

- [ ] **Step 8: Verify benchmark mode still needs nothing**

Run: `/tmp/monvark-t7 -B` for about fifteen seconds, then Ctrl+C.
Expected: it runs and reports a hashrate without asking for an address and
without starting a node.

- [ ] **Step 9: Verify the non-interactive error**

Run: `/tmp/monvark-t7 < /dev/null`
Expected: exits with an error saying stdin is not a terminal and naming
`--miningaddr` and the config file path. It must not hang.

- [ ] **Step 10: Run the whole suite and the linter**

Run: `./run_tests.sh`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add main.go config.go miner.go monitor.go sample-monvark.conf
git commit -m "main: Own the node from first run through to shutdown."
```

---

## Task 8: The release archive and the documentation

**Files:**
- Create: `.github/workflows/release.yml`
- Modify: `README.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: everything above.
- Produces: the published archives.

- [ ] **Step 1: Find the node release checksums**

Run:

```bash
gh release download v1.3.10 -R monetarium/monetarium-node \
  -p 'monetarium-node-linux-amd64' -p 'monetarium-node-windows-amd64.exe' \
  -D /tmp/mondl && sha256sum /tmp/mondl/*
```

Expected: two checksums. Record them; they go into the workflow verbatim. There
is no published checksum file upstream, which is exactly why the workflow pins
them itself: a changed asset then fails the build loudly instead of shipping
silently.

- [ ] **Step 2: Write the release workflow**

Create `.github/workflows/release.yml`, substituting the two checksums from
Step 1 for `PUT_LINUX_SHA_HERE` and `PUT_WINDOWS_SHA_HERE`:

```yaml
name: Release
on:
  push:
    tags: ['v*']
permissions:
  contents: write

env:
  # The node ships in the same archive as the miner.  Its asset is downloaded
  # at a pinned tag and checked against a checksum recorded here, because
  # upstream publishes no checksum file.  A changed asset fails this build
  # rather than shipping silently.
  NODE_TAG: v1.3.10
  NODE_SHA_LINUX: PUT_LINUX_SHA_HERE
  NODE_SHA_WINDOWS: PUT_WINDOWS_SHA_HERE

jobs:
  release:
    runs-on: ubuntu-24.04
    steps:
      - name: Check out source
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 #v7.0.1

      - name: Set up Go
        uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e #v7.0.0
        with:
          # The go.mod floor rather than 'stable', so every shipped platform is
          # built by the oldest toolchain the module claims to support.
          go-version-file: 'go.mod'

      - name: Cross-compile monvark
        env:
          CGO_ENABLED: '0'
        run: |
          set -eu
          mkdir -p dist/linux dist/windows
          GOOS=linux   GOARCH=amd64 go build -trimpath -o dist/linux/monvark .
          GOOS=windows GOARCH=amd64 go build -trimpath -o dist/windows/monvark.exe .

      - name: Fetch and verify the node
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -eu
          gh release download "$NODE_TAG" -R monetarium/monetarium-node \
            -p monetarium-node-linux-amd64 \
            -p monetarium-node-windows-amd64.exe -D nodedl
          echo "$NODE_SHA_LINUX  nodedl/monetarium-node-linux-amd64" | sha256sum -c -
          echo "$NODE_SHA_WINDOWS  nodedl/monetarium-node-windows-amd64.exe" | sha256sum -c -
          install -m 0755 nodedl/monetarium-node-linux-amd64      dist/linux/mond
          install -m 0755 nodedl/monetarium-node-windows-amd64.exe dist/windows/mond.exe

      - name: Assemble the archives
        run: |
          set -eu
          V="${GITHUB_REF_NAME}"
          # tar.gz for Linux because .zip does not reliably preserve the
          # execute bit, and zip for Windows because that is what it opens.
          tar -C dist/linux   -czf "monvark-${V}-linux-amd64.tar.gz" monvark mond
          (cd dist/windows && zip -q "../../monvark-${V}-windows-amd64.zip" monvark.exe mond.exe)
          sha256sum monvark-${V}-*.tar.gz monvark-${V}-*.zip | tee SHA256SUMS

      - name: Publish
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -eu
          V="${GITHUB_REF_NAME}"
          {
            echo "Contains \`monvark\` and \`mond\`.  Unpack anywhere and run \`monvark\`."
            echo
            echo '```'
            cat SHA256SUMS
            echo '```'
            echo
            echo "The Windows build is unsigned; see the README for the SmartScreen prompt."
          } > NOTES.md
          gh release create "$V" --title "$V" --notes-file NOTES.md \
            "monvark-${V}-linux-amd64.tar.gz" "monvark-${V}-windows-amd64.zip"
```

- [ ] **Step 3: Verify the workflow parses**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml'))" && echo OK`
Expected: `OK`.

- [ ] **Step 4: Rewrite the README's opening and requirements**

In `README.md`, replace the `## Requirements` section with:

```markdown
## Requirements

- An AMD or nVidia GPU with a working OpenCL driver.  That is the only thing
  you install.
- A Monetarium address to be paid to.  Create one with
  [monetarium-wallet](https://github.com/monetarium/monetarium-wallet) on any
  machine — **the wallet does not need to run while mining, and should not be
  installed on the mining machine at all.**  monvark needs the address as a
  string; it never holds keys.

You do **not** need to install or configure a node.  monvark ships with `mond`
and starts, supervises and shuts down its own, using a loopback port and
credentials it generates.  If this machine already runs a Monetarium node,
monvark leaves it alone and takes an ephemeral P2P port for its own.
```

- [ ] **Step 5: Replace the "Solo mining" section**

Replace `## Solo mining` in `README.md` with:

````markdown
## Running it

Download the archive for your platform from the releases page, check its
SHA256 against the release notes, unpack it anywhere, and run it:

```sh
tar xzf monvark-v2.0.0-linux-amd64.tar.gz
cd monvark-v2.0.0-linux-amd64
./monvark
```

On first run monvark asks once for the payout address, saves it to
`~/.monvark/monvark.conf` with mode 0600, starts the node, waits for the chain
to sync, and begins mining.  Later runs do not ask again.  Ctrl+C stops both.

To skip the question — under systemd or Docker, where there is nobody to ask —
pass `--miningaddr` or put `miningaddr=` in the config file.

What ends up on disk:

```
~/.monvark/
  monvark.conf   your settings, including the payout address
  mond.conf      the node's generated credentials; rewritten every start
  rpc.cert/.key  generated by the node
  node/          the node's data directory and logs
```

Once a block is found, monvark reads it back off the chain and confirms the
coinbase paid you.  Note that `CoinbaseMaturity` is 256, so the reward is not
spendable immediately.

The check confirms the address and catches configuration faults; it is not a
defence against a tampered `mond`, since a hostile node can lie on any RPC.
The archive's SHA256 is what covers that — verify it.
````

- [ ] **Step 6: Add the Windows section**

Add to `README.md`, after "Running it":

````markdown
## Windows

The Windows build is **unsigned**, so the first run takes three prompts:

1. SmartScreen shows "Windows protected your PC" — choose **More info**, then
   **Run anyway**.
2. Defender may quarantine the binary; miners are flagged by category rather
   than behaviour.  Restore it and add an exclusion for the folder you
   unpacked into.
3. Windows Firewall asks about network access when the node opens its P2P
   port.  Allow it, or the node will not find peers and will never finish
   syncing.

Verify the archive's SHA256 against the release notes before doing any of this:

```powershell
Get-FileHash monvark-v2.0.0-windows-amd64.zip -Algorithm SHA256
```
````

- [ ] **Step 7: Update `CLAUDE.md`**

In `CLAUDE.md`, replace the whole `## Outstanding at the end of this stage`
section with:

```markdown
## Outstanding

- **`cl/loader_windows.go` has never been executed on Windows.** It is verified
  today only by cross-compilation and `go vet`, never by an actual run against a
  Windows OpenCL driver.
- **The Windows build is unsigned.** No code-signing certificate is budgeted, so
  SmartScreen and Defender treat it by category; the README documents the
  workaround.
- **`getblocktemplate` does not exist on `monetarium-node`.** The payee check
  therefore reads a block back with `GetBlockVerbose` after submitting it,
  rather than inspecting a template beforehand.  It is evidence and a bug
  detector, not a defence against a substituted `mond`.
```

Then add, after the "Monetarium specifics" section:

```markdown
## Node ownership

monvark starts and owns its own node; there is no mode for attaching to one it
did not configure, because a `getwork` template pays the `miningaddr` of
whichever node served it.  `nodeStart` in `node.go` generates
`~/.monvark/mond.conf` at 0600 with the credentials and the loopback RPC port it
chose, and passes `--miningaddr` on the node's **command line**.

**That split is load-bearing, not stylistic.** The node picks uniformly at
random among its configured mining addresses
(`bgblktmplgenerator.go:728`), and go-flags *appends* command line occurrences to
config file entries rather than overriding them.  An address that ever lands in
both places therefore pays a random fraction of blocks elsewhere.  `mond.conf`
is checked for a `miningaddr` or `generate` key before being rewritten, and
their presence is an error rather than something to silently repair.

The default P2P port is left to whoever holds it: monvark test-binds it and
passes `--listen=:0` only when it is busy, so a machine already running a node
keeps working.  Shutdown is the node's own `stop` RPC via `RawRequest`, not a
signal — `os.Interrupt` cannot be delivered through `Process.Signal` on Windows.
```

- [ ] **Step 8: Run the whole suite and the linter**

Run: `./run_tests.sh`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add .github/workflows/release.yml README.md CLAUDE.md
git commit -m "build: Ship monvark and the node as one archive."
```

- [ ] **Step 10: End-to-end verification on a clean machine**

This is the check the spec's work order calls for and it cannot be automated
here. On a machine that has never run monvark:

1. download only the archive, verify its SHA256, unpack;
2. run `./monvark` and paste a payout address;
3. confirm the address is written to `~/.monvark/monvark.conf` at mode 0600, and
   that `~/.monvark/mond.conf` contains no `miningaddr`;
4. watch the sync line advance with a non-zero peer count, and confirm no device
   reports a hashrate before it completes;
5. confirm mining starts and the stats line names the payout address;
6. run a second time and confirm it does not ask again;
7. start a second Monetarium node on the default P2P port first, then run
   monvark, and confirm it warns about the ephemeral port and still starts.

---

## Definition of done

- [ ] `go build ./...` and `CGO_ENABLED=0 go build ./...` succeed, no build tags
- [ ] `./run_tests.sh` passes: tests and golangci-lint v2
- [ ] `-B` runs with no node, no address and no chain
- [ ] first run asks once, validates before writing, and writes 0600
- [ ] a flag that disagrees with the config file is an error naming the file
- [ ] a non-TTY with no address errors instead of hanging
- [ ] the node starts, syncs, and is shut down cleanly on exit
- [ ] monvark starts alongside a node already holding the default P2P port
- [ ] `mond.conf` never contains `miningaddr` or `generate`
- [ ] mining does not begin before the sync gate passes
- [ ] the coinbase of the first accepted block is verified against the address
- [ ] stale work re-issues `GetWork` without stopping the devices
- [ ] `--proxy*` and the RPC flags are gone from `--help`
- [ ] the payout address appears in the stats line and the status JSON
- [ ] the release workflow publishes both archives with their SHA256 sums
- [ ] README describes the real first run, including Windows

---

## Self-review

Checked after writing, against the spec.

**Spec coverage.** §2.1 payee split → tasks 3 (argv) and 5 (proof). §2.2 pinned
node asset → task 8. §2.3 unsigned Windows → task 8 steps 6 and 7. §2.4
credentials in a file, identity on argv → task 3. §2.5 expiry → task 6. §2.6
device ordering → task 1. §2.7 coexistence → task 3 steps 1 and 3. §3.1 startup
sequence → tasks 1 and 7. §3.2 layout → tasks 2, 3. §3.3 first run → task 2.
§3.4 node ownership → task 3. §3.5 sync gate → task 4. §3.6 work expiry →
task 6. §3.7 payee → task 5. §3.8 flag surface → task 7. §3.9 status → task 7
step 5. §3.10 packaging → task 8. §4 verification → the test steps in each task
plus task 8 step 10. §5 work order → the task order, which follows it.

**Type consistency.** `NewMiner` changes signature twice, in task 1 (devices,
workDone) and task 5 (payoutAddr); task 5 states the final form and task 7 calls
it. `onSoloWork` changes its last parameter from `[]*Device` to `*Miner` in task
6, which updates all three call sites in the same step. `coinbasePays` takes a
hex script string in both its test and its implementation.
`chainjson` is the import alias used in `node.go`, `node_test.go`, `miner.go`
and `miner_test.go` alike.

**Known rough edge, deliberately left to the executor.**
`TestValidatePayoutRejectsWrongNetwork` contains a hardcoded simnet address that
may not decode on this chain. Task 2 step 7 says what to do about it and why
loosening the test is the wrong repair.

**Not covered here, and deliberately:** restart-on-crash, an external-node mode,
multi-address payouts, and rig-manager monitoring APIs. All were ruled out in the
predecessor spec §6; the stage 2 spec §6 records the external-node opt-in as
considered-and-declined with the cost of adding it later.
