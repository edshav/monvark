# monvark

`monvark` is a BLAKE3 GPU miner for Monetarium. It mines solo on AMD and
NVIDIA cards through OpenCL, and ships with the node it mines against — so
there is nothing else to install and nothing to configure before the first
run.

No pool or stratum support, no CUDA build, no fan control or temperature
readout; use `nvidia-smi` (NVIDIA) or `rocm-smi` (AMD) for those.

## What you need

**A Monetarium address to be paid to.** Create one with
[monetarium-wallet](https://github.com/monetarium/monetarium-wallet) on some
other machine — the wallet is not needed while mining and should not be
installed on the rig. monvark only ever sees the address as a string; it never
holds keys.

You do **not** need to install or run a node. Every release archive contains
`mond` next to `monvark`, and monvark starts, supervises and shuts down its
own node on a loopback RPC port with credentials it generates. If this machine
already runs a Monetarium node, monvark leaves it alone and takes an ephemeral
P2P port for its own.

Binaries are published for **Linux x86-64** and **Windows x86-64**; on macOS
you [build from source](#macos).

## Download and run

Grab the archive for your platform from the
[releases page](https://github.com/edshav/monvark/releases). Each release lists
the SHA256 of its archives; check it before unpacking. The archive holds two
files, `monvark` and `mond`, with no wrapping directory — **keep them
together**, because monvark looks for `mond` beside its own executable.
Substitute the tag you downloaded for `<version>` below.

### Linux

```sh
sha256sum monvark-<version>-linux-amd64.tar.gz        # compare with the release notes
mkdir -p ~/monvark
tar -C ~/monvark -xzf monvark-<version>-linux-amd64.tar.gz
~/monvark/monvark
```

### Windows

```powershell
Get-FileHash .\monvark-<version>-windows-amd64.zip -Algorithm SHA256
Expand-Archive .\monvark-<version>-windows-amd64.zip -DestinationPath C:\monvark
C:\monvark\monvark.exe
```

The Windows build is **unsigned**, so the first run takes three prompts:

1. SmartScreen shows "Windows protected your PC" — choose **More info**, then
   **Run anyway**.
2. Defender may quarantine the binary; miners are flagged by category rather
   than by behaviour. Restore it and add an exclusion for the folder you
   unpacked into.
3. Windows Firewall asks about network access when the node opens its P2P
   port. Allow it, or the node will not find peers and will never finish
   syncing.

### macOS

macOS is a development target rather than a shipped one, so there is no archive
to download: you build monvark yourself (see
[Building from source](#building-from-source)) and fetch the node separately.

```sh
git clone https://github.com/edshav/monvark && cd monvark
go build .

# arm64 for Apple Silicon, amd64 for Intel
curl -LO https://github.com/monetarium/monetarium-node/releases/download/v1.3.10/monetarium-node-darwin-arm64
chmod +x monetarium-node-darwin-arm64

./monvark --mond=./monetarium-node-darwin-arm64
```

If you fetched the node with a browser rather than `curl`, clear Gatekeeper's
quarantine flag first:
`xattr -d com.apple.quarantine monetarium-node-darwin-*`.

Apple deprecated OpenCL back in 10.14, but the framework is still present and
monvark loads it. `-l` lists the CPU and the integrated GPU as OpenCL devices
alongside the discrete card, so pass `--devices` with the index you actually
want. Verified on an Intel Mac with a Radeon Pro. Apple Silicon compiles and loads the
same framework, but has not been run.

### What happens on the first run

monvark asks once for the payout address, saves it to `monvark.conf` with mode
0600, starts the node, waits for the chain to sync, and starts mining. Later
runs do not ask again. Ctrl+C stops the miner and the node together.

The first sync downloads the whole chain and takes a while; the miner waits for
it rather than mining on a stale tip.

To skip the question — under systemd or Docker, where there is nobody to ask —
pass `--miningaddr` or put `miningaddr=` in the config file.

Mainnet is the default. `--testnet` or `--simnet` mine on another network
instead.

Once, on the first block you find, monvark reads the block back off the chain
and confirms its coinbase paid you. Note that `CoinbaseMaturity` is 256 blocks,
so a reward is not spendable straight away.

### Don't hand it the whole machine

**By default monvark mines on every OpenCL device it can find** — not just the
graphics card. On a laptop or a desktop that usually means three: the CPU, the
integrated graphics that draw your screen, and the discrete GPU. `./monvark -l`
lists them exactly as monvark will use them:

```
DEV #0: Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz
DEV #1: Intel(R) UHD Graphics 630
DEV #2: AMD Radeon Pro 5300M Compute Engine
```

A GPU kernel cannot be preempted, so the display adapter stops repainting while
one runs and the machine looks frozen. **On a machine you use, name the discrete
card** by its index from `-l`:

```sh
./monvark --devices=2
```

## Checking your setup

Run a benchmark that needs no node and submits nothing, to confirm a device
actually hashes:

```sh
./monvark -B
```

`-D <index>` (the index from `-l`) benchmarks a single device. The reported
rate is a **cumulative average since the process started**, not an
instantaneous one — give it five minutes before reading anything into it.

## Files and settings

monvark keeps everything in one directory: `~/.monvark/` on Linux,
`%LOCALAPPDATA%\Monvark` on Windows, and
`~/Library/Application Support/Monvark/` on macOS.

```
monvark.conf   your settings, including the payout address
mond.conf      the node's generated credentials; rewritten on every start
rpc.cert/.key  generated by the node
node/          the node's data directory and logs
```

Every command line flag can go in `monvark.conf` instead, one `key=value` per
line. See [`sample-monvark.conf`](sample-monvark.conf) for the annotated list,
and `./monvark -h` for everything.

### Tuning

- `--intensity` — the work size for one kernel launch, as `2^intensity`
  (range 8–31). This is not a throttle: the mining loop never sleeps, whatever
  it is set to.
- `--worksize` — an explicit work size instead of a power of two; overrides
  `--intensity`.
- `--autocalibrate` — used when neither of the above is set: monvark sizes the
  work to spend about this many milliseconds per kernel launch (default 500).
- `--devices` — comma-separated device indices (from `-l`) to mine with. By
  default every device found is used, all at once — see [Don't hand it the
  whole machine](#dont-hand-it-the-whole-machine).
- `--dutycycle` — the percentage of the time a device hashes, 1–100 (default
  100). Below 100 it idles between kernel launches for as long as the arithmetic
  needs: at 50 it idles exactly as long as each launch took. This is the only
  setting that actually throttles a device. A found block is still submitted
  immediately, and the idle wait ends at once on Ctrl+C. New work is only picked
  up once the idle window is over, so a very low duty cycle means hashing a
  stale template for that much longer.
- `--opencl-lib` — full path to the OpenCL library, for installations the
  built-in search does not cover.

Each of `--intensity`, `--worksize`, `--autocalibrate` and `--dutycycle` takes
either one value for every device or a comma-separated value per device — so
`--dutycycle=25,100` throttles the first device hard and leaves the second
alone.

### Status API

`--apilisten` starts a read-only JSON status endpoint (default port 3333):

```sh
./monvark --apilisten=localhost
curl http://localhost:3333/
```

```json
{
  "validShares": 0,
  "invalidShares": 0,
  "totalShares": 0,
  "started": 1504453881,
  "uptime": 6,
  "payoutAddress": "MsMkhrc1z67m8iE56tFkDavsoHjeDiHFEwf",
  "devices": [{ "index": 0, "deviceName": "GeForce GT 750M", "hashRate": 110127366.53846154,
                "hashRateFormatted": "110.13 Mh/s", "started": 1504453880 }]
}
```

## Running unattended

Both of these need `--miningaddr` on the command line (or in `monvark.conf`):
neither a container nor a service unit has a terminal for the first-run prompt,
and monvark says so rather than hanging on a stdin that will never answer.

### Docker

There is no published image, and the Dockerfile is short enough to keep beside
the Linux archive you downloaded:

```dockerfile
FROM debian:bookworm-slim
# ocl-icd-libopencl1 is the loader monvark dlopens; the .icd file points it at
# NVIDIA's driver, which the NVIDIA Container Toolkit injects at run time.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ocl-icd-libopencl1 \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /etc/OpenCL/vendors \
 && echo libnvidia-opencl.so.1 > /etc/OpenCL/vendors/nvidia.icd
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility
ADD monvark-<version>-linux-amd64.tar.gz /opt/monvark/
ENTRYPOINT ["/opt/monvark/monvark"]
```

```sh
docker build -t monvark .
docker run --rm --gpus all monvark -B            # is the GPU visible at all?

docker run -d --name monvark --gpus all --stop-timeout 60 \
  -v monvark-data:/root/.monvark \
  monvark --miningaddr=MsMkhrc1z67m8iE56tFkDavsoHjeDiHFEwf
```

- **The volume is what keeps the chain.** The node's data directory is
  `/root/.monvark/node`; without a volume every restart resyncs from zero.
- **`--stop-timeout 60`** gives the node time to flush and exit on SIGTERM.
  Docker's default of 10 seconds would SIGKILL it mid-shutdown.
- **No published ports are needed.** The node dials out; inbound P2P is
  optional. Add `-p 3333:3333` only if you also pass `--apilisten=:3333`.

### systemd

```ini
[Unit]
Description=monvark BLAKE3 GPU miner
After=network-online.target
Wants=network-online.target

[Service]
User=miner
ExecStart=/opt/monvark/monvark --miningaddr=MsMkhrc1z67m8iE56tFkDavsoHjeDiHFEwf
Restart=on-failure
RestartSec=30
# monvark stops its node over RPC on SIGTERM; don't cut the shutdown short.
TimeoutStopSec=120

[Install]
WantedBy=multi-user.target
```

Save that as `/etc/systemd/system/monvark.service` and
`systemctl enable --now monvark`. Logging goes to stdout, so the journal has
it: `journalctl -u monvark -f`.

The service user needs access to the GPU, which on most distributions means
membership in `video` or `render`, and a home directory, since `~/.monvark` is
where the config and the chain end up. `Restart=on-failure` will not fight a
deliberate stop: monvark exits 0 when it is interrupted.

## Troubleshooting

**The machine froze, or the screen stopped redrawing, as soon as mining
started.** monvark took every OpenCL device, including the one drawing your
display. Restart with `--devices=` naming only the discrete card, and see
[Don't hand it the whole machine](#dont-hand-it-the-whole-machine).

**"No devices started", or the device fails while compiling the kernel.** The
OpenCL driver is missing or broken — a card that shows up in `-l` can still
fail here. Install the vendor driver, and if the library sits somewhere unusual
point at it with `--opencl-lib=/path/to/libOpenCL.so`.

**The node never finishes syncing.** It has no peers. Check that outbound
connections on port 9508 are not blocked, then supply your own way in with
`--addpeer=host:port` (repeatable) — a user-supplied list replaces monvark's
built-in bootstrap addresses, so a stale built-in one is something you can
route around.

**Nothing syncs on testnet or simnet.** Those networks carry no built-in
bootstrap addresses at all, so `--addpeer` is required there, not optional.

**"mond.conf contains ..., which monvark never writes".** Something added a
`miningaddr` or `generate` key to the node's generated config, either of which
would redirect the block reward. monvark refuses to start rather than quietly
overwrite it. Delete `mond.conf` and restart, or find out who added it.

**"no payout address configured and stdin is not a terminal".** monvark wanted
to ask for the address but is running under a service manager. Pass
`--miningaddr`, or add `miningaddr=` to `monvark.conf`.

For more detail on any of these, `-d debug` raises the log level.

## Building from source

Needs Go 1.23 or later. `go build .` is the whole build: there are no build
tags, nothing to `go generate`, and no cgo — the OpenCL library is resolved and
loaded at run time.

```sh
git clone https://github.com/edshav/monvark
cd monvark
go build .
```

A source build has no `mond` beside it, so point at one with `--mond
/path/to/mond` (or run `-B`, which needs no node).

## Contributing

```sh
./run_tests.sh          # what CI runs: go test -v ./... plus golangci-lint
go test -v ./...        # tests only
golangci-lint run       # config in .golangci.yml
```

Commit messages follow the Decred convention, `package/path: concise
description`. `CLAUDE.md` is the architecture map: what calls what, how the
work data is laid out, and which parts (the `blake3.cl` kernel, the BLAKE256
block-identity hash, the `dcr*` package names) are deliberate and should be
left alone.

Worth knowing before you measure anything: on a thermally limited card the
benchmark varies enough between runs that it cannot detect a code regression.
Use `device_test.go`'s GPU/host hash-agreement check for correctness instead.

Releases are cut by pushing a `v*` tag, which builds both platforms, fetches
the pinned `mond` release, verifies its checksum and publishes the archives.
Push tags by name — never `git push --tags`, since the repo also carries
upstream gominer's version tags locally.

## Credits

`monvark` is a fork of [decred/gominer](https://github.com/decred/gominer),
retargeted at Monetarium. It inherits gominer's GPL-3.0 license and its git
history — the upstream fork point is the tag `upstream-fork`, so
`git diff upstream-fork..main` is the Monetarium delta.
