# ADR-0003: Build from scratch; treat existing agents as a source of ideas

- **Status:** Accepted
- **Date:** 2026-10-31
- **Deciders:** Igor Artasevych
- **Spec:** [1.1](../spec.md#11-how-it-differs-from-neighbours)

## Context

The author runs a family assistant bot on OpenClaw, a Node.js gateway that
idles at about 1.2 GB of RAM, most of whose features are unused. The goal is
a lighter agent that also does what no existing assistant does: run tools
and compiled modules on edge nodes next to devices, route between models,
and survive network loss.

The "lightweight assistant in Go for boards" niche is already occupied by
PicoClaw (Sipeed, MIT, ~30k stars, v0.2.9): under 10 MB of RAM, ARMv7 and
RISC-V builds, 19+ channels, 30+ providers. It does not describe multiple
nodes, guaranteed delivery, a router driven by skill requirements, MCP
server mode, or a Go library API. It has no claude CLI provider because the
CLI is hundreds of megabytes and only builds for x64/arm64.

## Options

### A. Keep OpenClaw, cap its heap

### B. Fork PicoClaw and add distribution

### C. Build a new core from scratch

## Comparison

| Criterion | A. OpenClaw + cap | B. Fork PicoClaw | C. From scratch |
|---|---|---|---|
| Effort to first working bot | None | Low | High |
| Fit for distribution, delivery, routing | None: a monolith | Poor: single-node design, every core path changes | Designed in |
| Control over architecture | None | Constrained by upstream | Full |
| Upstream maintenance burden | None | Rebasing on a fast-moving project | None |
| Cost to run | Medium, still a large monolith | Low | Low |
| Learning value of the R&D | Low | Medium | High |

## Decision

**C.** The project's value is precisely the parts PicoClaw does not have, and
they reach into its core (delivery, history ownership, routing, node
registry). A fork would mean rewriting the core while carrying upstream
churn. We read PicoClaw and other agents for ideas and copy patterns, not
code.

The current bot stays in production as the fallback until the P1 gate.

## Consequences

- Chat, providers and channels must be kept minimal or they will eat the
  schedule (spec principle 7, "Not now" list).
- The stop criteria in spec section 13 apply: if P1 is not closed within
  four weekends, we stay on the current bot.

## Revisit when

P1 overruns its budget, or PicoClaw (or another project) ships multi-node
operation with guaranteed delivery.
