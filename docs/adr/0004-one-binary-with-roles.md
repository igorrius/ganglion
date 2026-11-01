# ADR-0004: One binary with roles; one process per node

- **Status:** Accepted
- **Date:** 2026-10-31
- **Deciders:** Igor Artasevych
- **Spec:** [5.1](../spec.md#51-roles-not-machines), [5.2](../spec.md#52-deployment-modes)

## Context

The system has three kinds of work: the core (history, memory, router,
delivery), the edge (tools and modules next to devices) and workers (model
execution). Deployments range from everything on one Raspberry Pi to a core
on a server with several edges at home. The MVP (P1) must run as one cheap
process; P2 must split across machines without a rewrite.

## Options

### A. One binary, roles enabled by flags, one process per node

`ganglion --roles core,edge,worker`. Distribution happens between nodes;
inside a node, non-WASM modules run as subprocesses.

### B. Separate binaries per role

`ganglion-core`, `ganglion-edge`, `ganglion-worker`.

### C. Several processes per node (a service per component)

## Comparison

| Criterion | A. One binary, roles | B. Binary per role | C. Process per component |
|---|---|---|---|
| All-in-one MVP on a Pi | One process | Three processes | Many processes |
| Path from P1 to P2 | Same code, different flags | Same, but three artifacts | Already split, heavy from day one |
| Release and install | One artifact per platform | Three per platform | Many |
| Edge binary size | Carries unused core code | Minimal | Minimal |
| Failure isolation inside a node | Process-level only; modules isolated by wazero and subprocesses | Per role | Per component |

## Decision

**A.** It makes the all-in-one MVP trivial and turns distribution into
configuration. Module isolation comes from wazero and subprocesses, not from
splitting the node. The extra code on an edge is acceptable while the
runner stays within the 15–30 MB RAM target (measured in P0, task 0.5).

## Consequences

- Role wiring goes through interfaces (`Transport`, `Queue`, `Provider`,
  `Router`), so a role talks to an in-process bus in P1 and to NATS in P2.
- `cmd/ganglion` is the only command; the `node` package assembles roles.

## Revisit when

The P0 measurement shows the edge binary or its RAM blocks small boards, or
a role needs an independent release cadence.
