# ADR-0107: Keep cells in a JetStream KV bucket, the only store of their values

- **Status:** Accepted
- **Date:** 2026-11-21
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-11, R-RUN-14), [7.8](../spec.md#78-security-and-operations-mvp) (R-BAK-1), [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules) (R-BUS-3, R-BUS-4); follows [ADR-0014](0014-state-cells.md), which put cells behind an interface "so P1 can use SQLite or memory and P2 NATS KV"; [design](../design/p2-bus-and-edge-design.md) section 5.4

## Context

Cells live in SQLite today, behind `cells.Service`. With edges (ADR-0105), a module on another
machine reports cells and a page on another machine reads them. ADR-0014 planned NATS KV for P2.

## Options

### A. Move cells into a KV bucket; KV is the only store

One JSON value per key `<module>.<cell>`; the key's revision is the cell's version; writes are
compare-and-set; history is the key's history.

### B. Keep cells in SQLite and publish each change on the bus

No migration; edges get changes as events.

### C. Leave cells alone for now

Only events between modules first, KV later.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| One source of truth across nodes | yes | no, two | yes, one node only |
| An edge module reports its cells | through the bus, as is | needs a separate path to the core | no |
| A page on an edge watches cells | a KV watch | a consumer of change events | no |
| Migration | once | none | later |
| History | 64 versions per key | SQLite, unlimited | SQLite |

## Decision

A (owner, 2026-11-21). It is the path ADR-0014 laid out, and the edge scenario needs it at once.

- Bucket `cells`, key `<module>.<cell>`, history 64; `/cells` shows the last 20 changes.
- The value: `desired`, `reported`, `cause`, `owner`, `kind`, `type`, `about`, `at`.
- Every write is a compare-and-set on the last revision; a lost race retries from a fresh read, at
  most 5 times.
- `cells.Service` keeps its interface; its store changes.
- At the first start, an empty bucket and a non-empty SQLite table: every cell is copied before
  modules start; the table stays read-only for one release, then goes.
- The backup archive gains an export of the bucket (values and history); a restore loads it before
  the node starts (R-BAK-1).

What changes against ADR-0014: nothing in its decision; it chose this. The SQLite store that P1 used
under the interface is retired.

## Security

- **Trust boundary:** cells written over the bus by edges (M10). Who may write which key is the
  edge's JWT permissions (ADR-0109): an edge writes only its own modules' keys.
- **What an attacker controls:** a compromised edge's writes to its own keys; a module's values, as
  today.
- **Abuse cases:** writing another module's cell, which the permissions refuse; a value of the wrong
  type, which the node's check refuses (R-RUN-14); a string value reaching the model, which taints as
  today (R-RUN-13).

## Consequences

- History beyond 64 versions is not kept; a module that needs a longer series keeps it itself (the
  web template's ring buffer in `state`).
- The backup gains a KV export step and its restore.

## Revisit when

A cell needs more than 64 versions of history in the node, or KV's compare-and-set retries show up as
a cost.
