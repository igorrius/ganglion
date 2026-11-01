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

To be filled with P0 results (tasks 0.2 and 0.3: the Deye monitor built both
ways).

| Criterion | A. Own ABI | B. Extism |
|---|---|---|
| Code to write and maintain | ABI, encoding, PDKs | Host functions only |
| PDKs for TinyGo / Rust | Ours to write | Provided |
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
