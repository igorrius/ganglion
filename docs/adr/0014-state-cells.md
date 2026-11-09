# ADR-0014: Flows act on state cells with swappable backends and observers

- **Status:** Accepted
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** [8](../spec.md#8-first-reflex-deye-p1), [9.6](../spec.md#96-flow-engine-p3), R-POL-5

## Context

The first flow (spec 8) was written around Tuya smart plugs: the flow
switched plugs directly. The plugs are not linked to the developer platform
yet, and tomorrow a user may want a Telegram message, a relay on an ESP32 or
a Home Assistant entity instead. The system must accept such a change — and
in P3 the agent must make it on request — without rewriting the flow.

Several parts of the plan already describe the same thing in different
words: kill switches as KV flags (R-POL-5), shadow mode (R-LS-3), manual
override (8.2), the event-replay simulator (9.6), node liveness and module
desired/actual state in KV (9.2), human confirmation with a `ttl` (7.6).

## Options

### A. Executors wired into the flow

The flow calls `tuya-plug` directly. Swapping means editing the flow.

### B. Named actions bound to executors in config

The flow emits "shed stage 1"; config maps it to `tuya-plug.off` or
`message.send`. Executors differ in nature (a plug has readable state, a
message does not), so the flow's rules apply unevenly.

### C. State cells

A cell is a piece of state with a **desired** value (what the flow wants), a
**reported** value (what is actually true), a version and history, kept in
KV with watch. A **backend** reconciles the two: `virtual` copies desired to
reported (optionally with injected delay, failure or a "manual" change),
`tuya`, `mqtt` or `ha` drive real devices. **Observers** react to changes —
a Telegram notification is an observer, not an executor. The pattern is the
device shadow / twin of AWS IoT and Azure, and matches how modules already
use desired/actual state (9.2).

## Comparison

| Criterion | A. Wired | B. Named actions | C. Cells |
|---|---|---|---|
| Swap plug → message → relay | Edit the flow | Change config | Change the backend or add an observer |
| Manual override detection | Plug-specific code | Only for some executors | Reported differs from desired without our command — uniform |
| Shadow mode | Special case | Special case | A `virtual` backend |
| Simulator and fault injection | Separate | Separate | `virtual` backend injects delay, failure, manual change |
| Kill switches, sensors, confirmations | Separate mechanisms | Separate | Cells of other kinds |
| Risk class | On the flow | On the action | On the backend binding: changing `virtual` to `tuya` raises it |
| Risk of over-engineering | Low | Low | Real: a general state-machine engine is a platform |

## Decision

**C** (accepted 2026-11-01), kept small:

- One data model for every cell: `desired`, `reported`, version, history,
  stored in KV. Behaviour is per kind.
- **P1 builds three kinds**, each needed by the first flow: **switch**
  (desired and reported on/off: loads, kill switches), **sensor** (reported
  only: grid present, SOC, node online) and **request** (a human
  confirmation: pending → approved, denied or expired by `ttl`).
- Backends in P1: `virtual` (with fault injection, used by tests, shadow mode
  and the simulator) and a notifier observer to Telegram. `tuya` comes when
  the plugs are linked; `mqtt` in P2, `ha` in P3.
- The risk class belongs to the backend binding. Binding a cell to a backend
  of a higher class needs a human's approval; the agent may propose it.
- Backends pass a shared conformance suite, like module hosts do.
- **No general state-machine language yet.** Generalise behaviour only after
  the three P1 kinds exist and a further, different case (the gate, the
  parking light) shows what is common — the rule of three.

## Consequences

- Spec 8 describes the flow in terms of cells; P1 can ship with the
  `virtual` backend and Telegram notifications, without plugs.
- Spec task 0.6 (Tuya access) moves to P1 and no longer blocks P0.
- Kill switches (R-POL-5) and the confirmation step (7.6) are implemented as
  cells.
- A cells package appears in P1, with KV storage behind an interface so P1
  can use SQLite or memory and P2 NATS KV.

## Revisit when

A fourth cell kind does not fit the model, or cells start needing
hierarchy (nested states) — then consider statecharts explicitly.
