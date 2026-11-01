# ADR-0005: Run compiled modules as WASM on wazero

- **Status:** Accepted
- **Date:** 2026-10-31
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules), [9.8](../spec.md#98-self-modification-levels)

## Context

The core hypothesis is that a regular task can be "compiled" by the model
into a small module that runs without the model. Today this already works
informally: the bot wrote a Python script (`deye.py`) that cron runs every
30 minutes. To generalise it, modules written by a model must:

- be sandboxed by capability (no network or files unless granted), because
  their code may have been influenced by prompt injection;
- run on ARM, x86 and RISC-V edges from one artifact;
- be small, fast to start, limited in memory and time;
- be swappable without downtime and without rebuilding the binary.

## Options

### A. WASM on wazero

Modules compiled from TinyGo, Rust or AssemblyScript; host functions granted
per manifest. wazero is pure Go, no CGO.

### B. Scripts as subprocesses (Python, shell)

What the bot does today.

### C. MCP server subprocesses

Any language, isolated by process, permissions in a manifest.

### D. Go `plugin` package / rebuilding the binary

## Comparison

| Criterion | A. WASM / wazero | B. Scripts | C. MCP subprocess | D. Go plugin / rebuild |
|---|---|---|---|---|
| Sandbox by default | Yes: only granted host functions | No: needs uid, Landlock or nsjail | Process + OS sandbox | None |
| One artifact for all CPUs | Yes | Needs an interpreter per node | Per-language runtime | No |
| Footprint per module | 1–16 MB, set by manifest | Interpreter (tens of MB) | Process + runtime | Whole binary |
| Memory and time limits | Built into wazero | OS limits | OS limits | None |
| Hot swap | Blue/green inside the runner | Restart the script | Blue/green process | `plugin` cannot unload |
| Languages for the model | TinyGo, Rust, AssemblyScript | Any | Any | Go only |
| Build toolchain on edge | Not needed: built on a worker | Interpreter needed | Runtime needed | Full Go toolchain |

## Decision

**A** for compiled reflexes, with **C** kept for things WASM cannot do well
(large libraries, existing MCP servers) and **B** only for skill scripts in
P1. **D** is rejected outright (see ADR-0008).

WASM wins on the two properties that matter most for model-written code
running next to physical devices: deny-by-default capabilities and one
small artifact for every edge CPU.

## Consequences

- A module ABI is needed: `init`, `handle`, `health` plus host functions.
  Own ABI vs Extism is decided in [ADR-0011](0011-module-abi.md).
- On 32-bit ARM and RISC-V wazero interprets instead of compiling; heavy
  modules belong on amd64/arm64 nodes.
- The model's language choice is narrower than with scripts.

## Revisit when

P0 shows the Deye monitor cannot be expressed cleanly in TinyGo, or wazero
memory overhead breaks the Pi Zero 2 W target.
