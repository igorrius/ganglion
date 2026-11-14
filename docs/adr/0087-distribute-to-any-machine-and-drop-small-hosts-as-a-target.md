# ADR-0087: Aim the node at any machine it is distributed to, and drop small hosts as a target

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** Igor Artasevych
- **Spec:** [1](../spec.md#1-what-we-are-building) (purpose: a distributed agent network), [2](../spec.md#2-principles) (principle 8), [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-6, target hosts), [10.2](../spec.md#102-p1--all-in-one--scope-and-acceptance) (P0 task 0.5, the Raspberry Pi run)
- **Supersedes:** the target part of [ADR-0056](0056-small-hosts-as-a-soft-target.md): small hosts (a Raspberry Pi, a small container) as the aim of principle 8, and device runs as acceptance. The `GOMEMLIMIT` rule of ADR-0056 stays.

## Context

The project's goal is a distributed agent network: a core and edge nodes, and
heavier work placed on machines with more power. The owner's direction on
2026-11-14 has three parts:

- Memory and size stay goals. Every dependency, runtime and module-language
  choice is still weighed on them, and a milestone still compares its numbers
  with the previous one.
- Small hosts stop being the aim. No measurement on a specific device is
  planned: not a Raspberry Pi, not Railway, not a Pi Zero. We do not yet know
  where the limit lies, so testing it now costs time with no decision behind it.
- The network runs where the owner wants it: a start-worker, a Kubernetes
  cluster, a large VM, or a small device, by the operator's choice. Nothing in
  the design assumes the cheapest host.

ADR-0056 set the aim at small hosts and made their measurements a condition
of acceptance (P0 task 0.5, the sandbox measurements on a Pi and on Railway,
the open lines in the README). Those conditions no longer guide the work.

Facts that still hold, from [ADR-0056](0056-small-hosts-as-a-soft-target.md):
Go's `GOMEMLIMIT` is a soft limit; `make memory` peaks are noisy around 128 MB;
the node idles near 32 MB RSS and the largest known peak is 99 MB on a hostile
page through `html2md`.

## Options

### A. Keep ADR-0056: small hosts are the aim, measured on devices

Principle 8 stays "small hosts are a target"; the Pi and Railway measurements
stay open work; features are placed by fit on a small host.

### B. Keep memory and size as goals; drop small hosts as the aim and device measurements as acceptance

Principle 8 says memory and size are weighed in every decision, each milestone
compares with the previous one, and a node runs where the operator places it.
No device measurement is required; the device-specific tasks are closed. The
`GOMEMLIMIT` rule stays, since a container limit is still a real limit.

### C. Drop the memory and size goals

Measure nothing in particular; size hosts to whatever the node uses.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Fits the distributed goal (any machine, start-workers, Kubernetes) | Biased toward small hosts | Yes | Yes |
| Effort on device measurements we do not need yet | High, open | None | None |
| Keeps memory and size in dependency decisions | Yes | Yes | No |
| Keeps a clear line for the next decision | A line we will not test | A rule to compare with the previous milestone | No line |
| Matches the owner's stated direction (2026-11-14) | No | Yes | No |

## Decision

B. The memory and size goals stay, and every decision about a dependency,
runtime or module language is still weighed on them. The small-host aim and
the device measurements go: the project does not spend time proving a limit it
does not yet need, and the network is built to run on any machine it is
placed on. `GOMEMLIMIT` from the config, or from the container's limit, stays.

## Security

No new boundary. Placing the node on a larger machine or in a cluster changes
where it runs, not what it trusts; the trust boundaries of ADR-0039, ADR-0077
and ADR-0081 are unchanged.

## Consequences

- Spec principle 8 says memory and size are weighed and compared with the
  previous milestone, and drops "small hosts are the aim". The target-hosts
  row of section 7.5 names "any machine the operator places a node on".
- Device-specific tasks are closed: the Raspberry Pi run and the Railway
  sandbox measurement (GitHub issues #42 and #43, closed 2026-11-14 as not
  planned for now). The phase plan's open items for them are dropped.
- The README's "open: Railway and a Pi not measured" lines are removed.
- What the node can run on a given machine is no longer a planned check. It
  is found when a feature fails on a machine someone actually runs.
- The memory tests keep reporting their numbers and do not fail on a fixed
  line; the milestone comparison stays.

## Revisit when

The owner names a concrete target host with a memory or size limit that the
node cannot meet with its features off. Then this decision is revisited, and
a device measurement is planned against that host.
