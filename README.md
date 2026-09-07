# monvark

`monvark` is a BLAKE3 OpenCL GPU miner for Monetarium. It mines solo, against
a `mond` node's `getwork` RPC — there is no pool/stratum support. It works
with both AMD and NVIDIA GPUs through OpenCL; there is no CUDA build.

Fan control and temperature reporting are not part of this miner. Use
`nvidia-smi` (NVIDIA) or `rocm-smi` (AMD) for that.

## Requirements

- A GPU vendor's OpenCL driver (AMD or NVIDIA). No OpenCL SDK or headers are
  needed to build — the library is loaded at run time, not linked in.
- Go 1.23 or later, to build. There are no build tags and no CUDA toolkit.
  `go build .` is the whole build.

## Building

```sh
git clone <this repo> monvark
cd monvark
go build .
```

The mining kernel is compiled into the resulting binary, so `monvark` can be
run from any directory — it does not need to find a kernel file on disk.

## Checking your setup

List the OpenCL devices monvark can see:

```sh
./monvark -l
```

Run a benchmark that needs no node and submits no work, to confirm a device
actually hashes:

```sh
./monvark -B
```

Add `-D <index>` (the index from `-l`) to benchmark one specific device. The
reported hash rate is a cumulative average since the process started, not an
instantaneous reading — let it run for at least five minutes before judging
the number.

## Solo mining

You need a Monetarium node (`mond`) that you already run and control.

1. **Disable CPU mining on the node.** A node with `generate=1` refuses
   `getwork` outright — "getwork polling is disallowed while CPU mining is
   enabled". Set `generate=0` in `mond`'s config, or omit the flag.
2. **Keep `miningaddr` set on the node.** It still needs it to build the
   coinbase output that pays out the mined block, even with CPU mining off.
3. **Point monvark at the node's RPC**, via flags or a config file (see
   `sample-monvark.conf`). The config file, if used, is `monvark.conf` under
   monvark's own application data directory (`~/.monvark` on Linux,
   `~/Library/Application Support/Monvark` on macOS,
   `%LOCALAPPDATA%\Monvark` on Windows) — `--configfile` overrides it:

   - `--rpcuser`, `--rpcpass` — the same credentials `mond` is configured with.
   - `--rpcserver` — the node's RPC address; defaults to `localhost`.
   - `--rpccert` — the node's RPC certificate. Defaults to `rpc.cert` under the
     node's own application data directory (`~/.monetarium` on Linux,
     `~/Library/Application Support/Monetarium` on macOS,
     `%LOCALAPPDATA%\Monetarium` on Windows).
   - `--testnet` / `--simnet` — select the network; mainnet is the default.

   RPC ports: **9509** mainnet, **19509** testnet, **19956** simnet.

4. Run it:

   ```sh
   ./monvark
   ```

## Tuning

- `--intensity` — sets the work size for one kernel launch, as `2^intensity`
  (range 8-31). It does **not** throttle the device or reduce heat: the mining
  loop has no sleep and runs the GPU at 100% duty cycle regardless of
  intensity.
- `--worksize` — an explicit work size instead of a power of two; overrides
  `--intensity`.
- `--autocalibrate` — when neither of the above is set, monvark sizes the
  work automatically to spend this many milliseconds per kernel launch
  (default 500).
- `--devices` — comma-separated device indices (as shown by `-l`) to mine
  with. All devices found are used by default, running concurrently.
- `--opencl-lib` — full path to the OpenCL library, for installations where
  the built-in search does not find it.

## Status API

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
  "devices": [{
    "index": 0,
    "deviceName": "GeForce GT 750M",
    "hashRate": 110127366.53846154,
    "hashRateFormatted": "110.13 Mh/s",
    "started": 1504453880
  }]
}
```

## Credits

`monvark` is a fork of [decred/gominer](https://github.com/decred/gominer),
retargeted at Monetarium. It inherits gominer's GPL-3.0 license and its git
history — the upstream fork point is the tag `upstream-fork`, so
`git diff upstream-fork..main` is the Monetarium delta.
