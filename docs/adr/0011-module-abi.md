# ADR-0011: Module ABI — own host interface or Extism

- **Status:** Accepted (own ABI)
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules), R-RUN-5, P0 task 0.3

## Context

[ADR-0005](0005-wasm-on-wazero-for-modules.md) chose WASM on wazero. A
module needs a way to receive input, return output and call host functions
(`log`, `state.*`, `http.fetch`, `secret.get`, `config.get`, `now`), each
checked against its manifest. Model-written modules (P3) may come in several
languages, so a PDK per language is needed either way.

P0 built both variants end to end and measured them with three toolchains.

## Options

### A. Own ABI directly on wazero

Host functions in a `ganglion` import module; JSON across the boundary;
variable-length answers copied out with `take` so the host never allocates
in module memory. Host side: `runner`. Module side: `pdk/guest` (Go,
TinyGo), `modules/deye-rs/src/guest.rs` (Rust); named `pdk/ownabi` and
`own.rs` during the comparison.

### B. Extism

A plugin framework on wazero. Go SDK v1.7.1 (BSD-3) on the host; PDKs for
Go/TinyGo, Rust and other languages. HTTP, config and log are built in;
state and secrets are custom host functions in `extism:host/user`. Host
side: `runner/extismrun`. Module side: `pdk/extismabi`,
`modules/deye-rs/src/extism_glue.rs`. Removed after this decision; the code
is in commit 89db64f.

## What was built and checked

- Both hosts pass the same conformance suite (`runner/runnertest`, 11
  checks: every host function, manifest denials, timeout, memory limit).
- The `deye-reader` module exists in six variants: {own, Extism} ×
  {Go, TinyGo, Rust}. All six read the live DeyeCloud API on 2026-11-01 and
  returned identical values in all 12 fields for the same collection time;
  the Go builds also matched the production `deye.py` exactly (task 0.2).

## Measurements

`cmd/modbench`, 2026-11-01, amd64 (Intel Core Ultra 7 265U), wazero 1.12,
300 calls per variant, each call in a fresh instance with one HTTP request to
a local server replaying a real 4 KB `/device/latest` response. Two runs;
ranges show both. Raspberry Pi numbers come in task 0.5.

| Variant | `.wasm` | Cold load | Call p50 | Call p95 | RSS after calls |
|---|---|---|---|---|---|
| own · Rust | 161 KB | 46–70 ms | 1.8–3.7 ms | 3.7–6.2 ms | 20 MiB |
| own · TinyGo | 669 KB | 490–513 ms | 7.5–7.8 ms | 12 ms | 25 MiB |
| own · Go | 3.6 MB | 0.84–0.93 s | 22–24 ms | 36–39 ms | 41 MiB |
| Extism · Rust | 252 KB | 112–115 ms | 4.4–5.2 ms | 6.9–9.0 ms | 22 MiB |
| Extism · TinyGo | 688 KB | 426–532 ms | 7.4–8.4 ms | 11.5–12.9 ms | 27 MiB |
| Extism · Go | 3.6 MB | 0.93–1.05 s | 22 ms | 32–34 ms | 42 MiB |

RSS includes the Go host process (about 12 MiB before loading anything).

## Comparison

| Criterion | A. Own ABI | B. Extism |
|---|---|---|
| Conformance suite | Passes | Passes |
| Module size | Smaller: Rust 161 KB vs 252 KB; TinyGo and Go within 3% | Adds the Extism kernel to every instance |
| Call latency | Equal for Go and TinyGo; Rust about 2× faster | Equal for Go and TinyGo; Rust slower |
| Host memory | 1–2 MiB less | — |
| HTTP | Our `http.Client`: timeouts, proxies, test doubles, policy and audit hooks per request | Always `http.DefaultClient`; nothing can be injected without replacing the global (the benchmark had to) |
| Denied or failed request | Module gets an error and decides (e.g. report "no data" during an outage) | `panic` in the host function traps the whole call; the module cannot react |
| Our policy layer (R-POL-1) on HTTP | Every request passes our code | Only Extism's `allowed_hosts` glob; no per-request hook |
| PDKs | Ours: Go/TinyGo glue ~130 lines, Rust glue ~90 lines; each new language needs one | Provided for many languages |
| Dependency health | None | Go SDK: last release 2025-03, last commit 2025-05, built against wazero 1.9; ~186 stars. The Extism core is active (2026-10) |
| Extra dependencies | None beyond wazero | gobwas/glob, OpenTelemetry protos, protobuf, wabin |

## Decision

**A, own ABI** (accepted 2026-11-01). Both variants work, but the deciding
differences favour A:

1. **Policies need to see every request.** Physical-action and network
   policies are enforced by the node that executes (R-POL-1). With Extism the
   HTTP path runs inside the SDK with a fixed global client, so per-request
   policy, audit, timeouts and proxies cannot be added without forking it.
2. **Failure handling.** A network failure is normal for an edge module (the
   Deye reader runs exactly during power cuts). Extism turns it into a trap
   of the whole call; with A the module handles it.
3. **Dependency risk.** The Extism Go SDK has been idle for 18 months and is
   built against an older wazero.
4. **Cost of A is small and already paid** for Go, TinyGo and Rust; the ABI
   is language-neutral (a handful of imports, JSON), so another PDK is a
   one-evening job.

What we give up: ready-made PDKs for languages we have not covered
(AssemblyScript, Zig, JavaScript…). If P3 shows the model needs one of those,
writing its PDK is part of that work.

## Consequences

- `runner` (own ABI) is the module host from P1 on; the wire format becomes
  versioned and documented in `pdk`.
- Done: `runner/extismrun`, `pdk/extismabi`, `modules/deye/extism` and the
  Extism Rust glue were removed together with the Extism dependencies; the
  conformance suite (`runner/runnertest`) stays for future hosts.
- Language for model-written modules stays open (spec section 11) and is
  decided at the start of P3. These numbers favour Rust or TinyGo for edge
  modules: standard Go modules are 5–20× larger, slower to load, and cost
  about 20 MiB more host memory each.

## Revisit when

The Extism Go SDK becomes active again and offers an injectable HTTP client
and non-trapping errors, or P3 needs several languages we have no PDK for.
