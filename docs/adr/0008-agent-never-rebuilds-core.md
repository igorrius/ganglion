# ADR-0008: The agent extends the system but never rebuilds the core

- **Status:** Accepted
- **Date:** 2026-10-31
- **Deciders:** Igor Artasevych
- **Spec:** [9.8](../spec.md#98-self-modification-levels), principle 4

## Context

The agent is meant to grow its own abilities: write skills, scripts and
modules. The tempting extreme is letting it edit and rebuild the Go core
itself to add features without downtime.

## Options

### A. Levels 1–4 only: skills, scripts, MCP modules, WASM modules

### B. Also level 5: `go build` inside the environment and swap the binary

### C. Go `plugin` package for hot-loaded Go code

## Comparison

| Criterion | A. Levels 1–4 | B. Rebuild core | C. Go plugin |
|---|---|---|---|
| Isolation of agent-written code | Per level: none needed, sandbox, process, capabilities | None | None, same address space |
| Recovery from a bad change | Roll back the module or `git revert` | Bot may not start; the repairer is the bot | Process crash |
| Image size | Unchanged | +~0.5 GB Go toolchain | Toolchain must match exactly |
| Zero-downtime updates | Blue/green per module | Binary swap | No unloading |
| Platforms | All | All | Linux only |

## Decision

**A.** The core is immutable to the agent. Everything the agent adds lives
in git and in modules that can be rolled back. The zero-downtime benefit of
B comes from blue/green module replacement instead.

## Consequences

- Every new capability must be expressible as a skill, script, MCP module or
  WASM module; the core must expose enough extension points for that.
- Core changes are made by people (or by coding agents in this repository
  under review), never by the running agent.

## Revisit when

Never by default. Only if a whole class of needed capabilities cannot be
built as modules.
