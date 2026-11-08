# ADR-0056: Treat small hosts as a soft target held by GOMEMLIMIT, not as a 128 MB line

- **Status:** Accepted
- **Date:** 2026-11-08
- **Deciders:** Igor Artasevych
- **Spec:** [2](../spec.md#2-principles) (principle 8), [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-6, target hosts), [10.2](../spec.md#102-p1--all-in-one--scope-and-acceptance); the global constraints of the [P1 phase plan](../plans/p1-all-in-one.md#global-constraints)
- **Supersedes:** the reading of principle 8 as a 128 MB budget that a node must stay under, used as a line in [ADR-0026](0026-opentelemetry-prometheus-and-promtop.md), [ADR-0033](0033-memory-in-the-workspace-git-with-the-git-binary.md), [ADR-0035](0035-web-reading.md) and the memory tests

## Context

The 128 MB figure came from P0 task 0.5: measure the runner "on a Pi or in a
container limited to 128 MB". After the measurement it became principle 8,
"a small container of about 128 MB", and every milestone since has measured
RSS against it. The memory tests (`make memory`) fail when a peak passes
128 MB.

What the line has cost and shown:

- The node itself is small: 32 MB RSS idle, about 45 MB after 1,000 turns
  that each call a tool, a 46 MB binary.
- Single peaks reach the line: one hostile 1 MiB page through `html2md` peaks
  at 99 MB; the `/metrics` picture peaks at 122–134 MB on `master` and on
  branches alike, so the test fails on noise and no longer separates a change
  from its base. The workaround is `GOMEMLIMIT=80MiB`.
- No real host asks for 128 MB. The spec's own named target is a Pi Zero 2 W
  with 512 MB; Railway bills memory by use and has no 128 MB tier.

Go has no hard memory cap. `GOMEMLIMIT` is a soft limit: the collector works
harder as the heap nears it, and the process may still pass it. The owner,
2026-11-08: the agent should run on minimal devices, held there by
`GOMEMLIMIT`; whatever does not fit a small host does not run on it. In the
network (P2) the core can live on a small device while heavier work runs on a
denser edge or a worker, by the operator's choice, not by a rule.

## Options

### A. Keep the 128 MB line

Principle 8 stays a budget; the tests keep failing above 128 MB; features that
peak above it (pictures, large pages) are cut or tuned until they fit.

### B. A soft target held by GOMEMLIMIT, with work placed where it fits

Small hosts stay the aim, and memory is still measured and weighed in every
decision about dependencies, runtimes and module languages. Each milestone
records its numbers and compares them with the previous milestone, not with a
fixed line. A node sets `GOMEMLIMIT` from its config, or, when none is set, from
its container's memory limit. A feature that needs more than a small host has
is allowed: it runs on a denser node, and a small node runs without it.

### C. Drop the memory goal

Measure nothing in particular; size hosts to whatever the node uses.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Runs on a Pi or a small container | Yes, by cutting features | Yes, with what fits | Not watched |
| Tests separate a regression from noise | No: `/metrics` peaks fail on `master` too | Yes: compared with the previous milestone | No measurement |
| Features like pictures and large pages | Cut or tuned to the line | Allowed; placed where they fit | Allowed |
| Matches how Go manages memory | No: treats a soft limit as hard | Yes | — |
| Fits the network of P2 (core small, heavy edges) | Every node held to one line | Yes | Yes |

## Decision

B. The line was an assumption from P0 that no host asks for, and it now fails
tests on noise; a soft limit is what Go offers, and the network of P2 lets heavy
work live on a node that has the memory.

## Security

No new boundary. The container's limit is read from the kernel's cgroup files,
which only someone in control of the host can change.

## Consequences

- Principle 8, R-RUN-6, the 7.5 table and the phase plan's constraints say
  "target, measured", not "budget".
- The memory tests report their numbers and no longer fail above 128 MB;
  `docs/README.md` records them per milestone beside the previous ones.
- R-RUN-6 covers every node, not only the runner: `GOMEMLIMIT` from the config,
  else about 80% of the container's memory limit (cgroup v2 `memory.max`, v1
  `memory.limit_in_bytes`), else none. Built in M4 task 4.1, with no new
  dependency.
- A growth between milestones still needs a reason, as before.
- What a small node cannot run is not yet chosen per feature; it is when a
  feature first does not fit.

## Revisit when

A real host the owner wants to run on has less memory than the node needs with
its features off, or `GOMEMLIMIT` cannot keep a node out of the kernel's OOM
killer on one.
