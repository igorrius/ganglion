# ADR-0058: Take load shedding out of P1 and pass the gate without the two-week clock

- **Status:** Accepted (owner, 2026-11-09)
- **Date:** 2026-11-09
- **Deciders:** Igor Artasevych
- **Spec:** [8](../spec.md#8-first-reflex-deye-p1) (R-DEYE-2, R-LS-4), [9.6](../spec.md#96-flow-engine-p3) (R-LS-1…3), [10](../spec.md#10-plan-phases-and-gates), [10.2](../spec.md#102-p1--all-in-one--scope-and-acceptance); [M4 design](../design/p1-m4-reflexes-design.md) sections 1 and 9; [P1 phase plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-all-in-one.md) (M4, M5, D-4); [ADR-0014](0014-state-cells.md)

## Context

P1 was planned as the agent plus its first reflexes:
- M4: the `deye-reader` module runs inside the node on cron and writes the
  battery's state of charge and the grid status into sensor cells;
- M5: a load-shedding flow (spec 8) reads those cells and switches two
  boilers through switch cells, after a week of shadow mode;
- the P1 gate: "Deye runs two weeks without the model", plus the family
  bot migrated and cheaper.

What stood behind it, as of 2026-11-09:
- The Tuya plugs are not linked (task 0.6 moved to P1). The loads would be
  `virtual` switch cells only (ADR-0014): shadow mode with no device
  behind it.
- The 5-minute polling without grid (spec 8.2) exists only for load
  shedding: at 5 kW the battery loses 10 % in about 12 minutes, and the
  flow has to see it. Monitoring alone needs 30 minutes.
- The flow was to be one hand-written state machine (D-4). The owner sees
  load shedding as one case of something larger: ready modules linked
  through cells, edited visually in the spirit of Node-RED, by a person or
  by the model. That is the flow engine of spec 9.6, not a P1 module.
- The two-week clock measures a module that stays untouched. The owner's
  frame (brainstorm, 2026-11-09): a module is code like any other; bugs
  and exceptions are handled and new versions ship. What matters is when
  it started and a record of what changed and why.

## Options

### A. Keep M5 and the gate as written

Build the load-shedding flow in P1 on `virtual` cells, run it a week in
shadow mode, enable it, and pass the gate after two weeks of Deye running
without the model. Deye polls every 5 minutes without grid.

### B. Load shedding to the far spec; the gate on a start point and a journal

Spec 8's flow, R-LS-1…3 and M5 leave P1. They return next to spec 9.6 as
flows of ready modules linked through cells, with a visual editor, written
by a person or the model. The gate replaces the clock with "the Deye module
runs on cron without the model; its versions and failures are journaled"
(the journal is [ADR-0061](0061-local-modules-built-from-source.md)).
Deye polls every 30 minutes.

## Comparison

| Criterion | A. Keep M5 and the clock | B. Far spec, start point and journal |
|---|---|---|
| What P1 proves about reflexes | A module and a flow run without the model | A module runs without the model, and its changes are recorded |
| Physical effect in P1 | None: the loads are `virtual` without plugs | None |
| Fits "a module is code that keeps improving" | No: a fix in week two either restarts the clock or is held back | Yes: every version is a journal row |
| Work left in P1 | M5: the state machine, the notifier, kill switches, shadow week, the clock | None for flows |
| What a later flow engine reuses | A hand-written flow it would replace | Cells owned by modules ([ADR-0062](0062-cells-owned-by-modules.md)), the link it is built on |
| DeyeCloud calls a day | Up to 288 | 48 |
| Time to the gate | At least three more weeks after M4 | When the journal and the migration are done |

## Decision

**B** (owner, 2026-11-09). A flow engine is the real target, and a
hand-written flow on cells with no device behind them proves little of it.
A clock that asks for two weeks without change contradicts how modules are
meant to live.

- Spec section 8's flow, R-LS-1…3 and the M5 milestone leave P1. They go
  to the far part of the spec, next to 9.6, reworded as flows of ready
  modules linked through cells with a visual editor.
- **R-LS-4 stays** as a migration check: the module's readings are compared
  with `deye.py` for a few days on Railway before the family bot moves.
- **The gate:** the Deye module runs on cron without the model, and its
  versions and failures are journaled; the family bot has migrated and
  costs less than the current setup.
- **Polling:** every 30 minutes. The 5-minute cadence without grid leaves
  with load shedding.
- D-4 (the flow as a WASM module or native Go) leaves P1 with M5.

## Security

No new boundary. P1 loses the only path by which a module would have
driven a physical load; switch cells written by modules wait for the flow
engine.

## Consequences

- P1 has no physical action. Kill switches and switch cells written by
  modules are not built in P1; `cell.set` waits for the first module that
  needs it.
- M4 is rewritten around the node config, modules built by the agent and
  cells as registers ([M4 design](../design/p1-m4-reflexes-design.md),
  section 12); M5 is removed from the phase plan and the board.
- The P1 row of spec section 10 and the 10.2 acceptance change: no shadow
  week and no "enabled" flow.
- 10.2's acceptance for M6 takes its example from a module's setting or a
  limit instead of a flow.
- Cron polling takes about 1.5 % of DeyeCloud's free 100,000 calls a month,
  down from about 9 % in the worst case.
- During an outage the bot's battery figure can be up to 30 minutes old;
  `deye-reader.status` reads fresh on request.

## Revisit when

- The plugs are linked and the owner wants loads switched before the flow
  engine exists.
- Work on the flow engine (9.6) starts: it takes R-LS-1…3 back as its first
  case.
