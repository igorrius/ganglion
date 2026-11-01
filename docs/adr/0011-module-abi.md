# ADR-0011: Module ABI — own host interface or Extism

- **Status:** Proposed — decided by P0 task 0.3
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules), R-RUN-5

## Context

[ADR-0005](0005-wasm-on-wazero-for-modules.md) chose WASM on wazero. A
module exports `init`, `handle(trigger, payload) → result` and `health`,
and sees only the host functions its manifest grants (`log`, `state.*`,
`http.fetch`, `event.emit`, `tool.call`, `secret.get`, `now`). We need a
way to pass data across the boundary and a PDK for TinyGo and Rust.

## Options

### A. Own ABI directly on wazero

Define memory layout, encoding and host functions ourselves.

### B. Extism

A plugin framework on wazero with PDKs for TinyGo and Rust. Go SDK v1.7.1,
BSD-3. Its manifest already supports `allowed_hosts`, `max_pages`,
`max_http_response_bytes`, `timeout_ms`, host functions and a config map;
it has a compilation cache and reusable compiled plugins; WASI is opt-in.

## Comparison

To be completed with P0 results. Task 0.3 builds the Deye reader under both
ABIs with three toolchains — TinyGo, standard Go and Rust — and fills a
2 × 3 table of module size, call time and runner RAM. Rust is included
because it has no runtime or garbage collector, so it is expected to give
the smallest modules; with the own ABI it needs our own small crate for the
host imports, with Extism it uses the provided Rust PDK.

**Own ABI, measured 2026-11-01** (task 0.2, `deye-reader`, wazero 1.12,
amd64, one call that stops at a missing secret, before any network):

| Build | `.wasm` size | Call incl. fresh instance |
|---|---|---|
| Go 1.26 (`GOOS=wasip1`, `-buildmode=c-shared`) | 3.8 MB | ~6 ms |
| TinyGo 0.44 (`-opt=2 -no-debug`) | 0.69 MB | ~1 ms |

The own ABI took about 130 lines of module-side glue (`pdk/wasm_wasip1.go`)
and 120 lines of host functions (`runner/runner.go`). TinyGo needed one
change: imported host functions cannot be used as values.

| Criterion | A. Own ABI | B. Extism |
|---|---|---|
| Code to write and maintain | ABI, encoding, PDKs | Host functions only |
| PDKs for TinyGo / Rust | Ours to write (Go done in P0; a Rust crate of about 100 lines) | Provided |
| Fit with our manifest and limits | Exact | Close: allowed hosts, memory, timeout |
| Dependency risk | None | Small community (~190 stars on the Go SDK) |
| Module size and startup | Measure in P0 | Measure in P0 |
| Runner RAM | Measure in P0 | Measure in P0 |

Either way, signing, desired state, the supervisor and policies are ours.

## Decision

Pending. Leaning towards B if the P0 measurements show no significant size
or RAM penalty and the dependency looks maintainable.

## Consequences

To be written with the decision.

## Revisit when

Set when accepted.
