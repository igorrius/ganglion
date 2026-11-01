# Runner memory: wazero modules and a JetStream leaf node under 128 MB

- **Date:** 2026-11-01
- **Task:** P0 · 0.5 ([spec 10.1](../spec.md#101-p0--recon--tasks-and-acceptance))
- **Question:** does an edge runner — wazero with modules plus an embedded
  NATS leaf node with JetStream — fit a small board, and in how much memory?
- **Status:** measured on **amd64** inside a 128 MB memory limit — itself a
  target host (spec principle 8). A Raspberry Pi run later confirms arm64
  timings (see the end); it is not a gate. Memory on amd64 and arm64 is comparable — both are 64-bit, Go and
  NATS use the same data structures — but wazero's machine code and all
  timings will differ on a Pi.

## Setup

- Host: Intel Core Ultra 7 265U, Linux, Go 1.26.3, wazero 1.12.
- Modules: `deye-reader` built with Go, TinyGo and Rust (ADR-0011).
- Runner: `cmd/modbench` — the real `runner` package, 300 calls, each in a
  fresh instance, HTTP answered from a recorded 4 KB DeyeCloud response.
  Run as `systemd-run --user --scope -p MemoryMax=128M -p MemorySwapMax=0`.
  The limit was checked to be enforced: the Go module under a 48 MB limit
  was killed (exit 137), the Rust module ran at 48 MB.
- NATS: `nats:2.15.2-alpine` in Docker with `--memory 128m`, a core server
  with JetStream and an edge leaf node with JetStream (own domain) connected
  to it. Load with `nats bench js pub async`, 1 KB messages, file storage,
  deduplication on. RSS read from `/proc/1/status` (`VmRSS`, `VmHWM`).

## Runner with one module (128 MB limit)

| Module | Process at start | After load | After 300 calls | Peak | Go heap |
|---|---|---|---|---|---|
| Rust (161 KB) | 10 MiB | 19 MiB | 17 MiB | **23 MiB** | 2 MiB |
| TinyGo (669 KB) | 11 MiB | 61 MiB | 23 MiB | **77 MiB** | 3 MiB |
| Go (3.6 MB) | 14 MiB | 70 MiB | 38 MiB | **78 MiB** | 9 MiB |

All three ran without failures under the limit. The peak is compilation:
wazero compiles the module to machine code once at load; afterwards memory
settles. Rust modules keep the whole runner near 20 MiB.

## NATS leaf node with JetStream (128 MB limit)

| State | Leaf RSS | Core RSS |
|---|---|---|
| Idle, leaf connected | 18 MiB | 18 MiB |
| After 10,000 × 1 KB (11 MB stored) at ~90k msg/s | 49 MiB peak | 18 MiB |
| After 110,000 × 1 KB (117 MB stored), default settings | **80 MiB**, not released after 20 s idle | — |
| Same 110,000 with `GOMEMLIMIT=40MiB` | **46 MiB** peak, 45 MiB idle, same write speed | — |

Without a limit the Go runtime keeps memory after a burst; a soft limit
(`GOMEMLIMIT`) holds a large backlog well under 50 MiB with no visible cost.
Embedded in the runner, NATS shares one Go runtime, so the same soft limit
applies to the whole process.

## Binaries (cross-compiled for linux/arm64, stripped)

| Binary | Size |
|---|---|
| `ganglion` today (no wazero, no NATS yet) | 2.1 MB |
| `modbench` (runner + wazero) | 8.8 MB |
| `nats-server` 2.15.2 (amd64, as a reference for embedding) | 17 MB |

An edge binary with the runner and embedded NATS should land around
25–30 MB, matching the original estimate.

## Estimate for an edge node

Runner + embedded leaf node + a few small modules, with `GOMEMLIMIT` set:
about **40–60 MiB steady**, with short peaks while a large module compiles.
Standard Go modules cost about 20 MiB more each and peak near 80 MiB at
load; on a 512 MB Pi Zero 2 W that still fits, but it argues for Rust or
TinyGo modules on small edges.

## Requirements this adds

- The runner sets a soft memory limit (`GOMEMLIMIT`) from its config.
- Edge JetStream streams get explicit size limits, so a long outage cannot
  grow the backlog without bound.

## Still to do on a real Pi (arm64)

- Load time of each module (compilation on a Cortex-A53 is expected to be
  several times slower than here).
- Call latency and the memory figures above, to confirm the amd64 proxy.
- 32-bit ARM is not covered: wazero interprets there instead of compiling.
