# ADR-0015: Build the agent early in P1 and let it reconfigure the system

- **Status:** Accepted
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** [1](../spec.md#1-what-we-are-building), [9.8](../spec.md#98-self-modification-levels), [10](../spec.md#10-plan-phases-and-gates), [P1 plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-all-in-one.md)

## Context

ganglion is an agent: a model-driven assistant that designs and maintains
its own automations. Cells, reflexes (modules) and delivery are its body.

The first P1 plan put the infrastructure first — cells, delivery, runner,
the Deye flow — and the agent fifth, after about 2.5 weekends. That order
risks building a home-automation runtime with an agent bolted on, and it
delays the part of the P1 gate that is about the agent: the family bot is
replaced and has not got worse. P1 also contained nothing agentic beyond
conversation: the agent neither designs nor changes the system until P3.

## Options

### A. Keep the infrastructure-first order

### B. Agent right after its foundations, plus configuration-level self-modification in P1

Order: cells → delivery and Telegram → **agent** → runner and Deye → flow →
**agent reconfigures the system** → migration. The agent can change the
system's configuration — observers, thresholds, cell backends — through a
proposal the owner approves, committed to git and reversible. Writing
modules stays in P3.

### C. Agent first, before cells and delivery

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| A talking agent on the test bot | After ~2.5 weekends | After ~2.5 weekends of which the agent is the third milestone; reflexes plug in as its tools | Earliest, but without guaranteed delivery or confirmations |
| Reflexes and cells built for the agent's use | Retro-fitted | From the start | Retro-fitted |
| Something agentic beyond chat in P1 | No | Yes: approved reconfiguration | No |
| Risk to the P1 gate | Agent quality tested late | Balanced | Delivery and policies rushed |
| Cost | — | +0.25–0.5 weekend for reconfiguration, offset by moving the claude CLI worker to P2 | — |

## Decision

**B.** The agent comes third, on top of the two foundations it needs (cells
for confirmations, delivery for not losing messages). The reflexes that
follow are wired to it as tools from day one. P1 adds a small, real
self-modification loop at the configuration level (level 0 in spec 9.8):
the agent proposes a change, the owner approves through a request cell, the
change is validated, applied, committed and can be rolled back.

The claude CLI worker moves to P2; routine work never needed it, and heavy
tasks (writing modules) belong to P3 anyway.

## Consequences

- The P1 plan is reordered; the Deye "two weeks without the model" clock
  starts at the runner milestone, about half a weekend later than before.
- Configuration becomes versioned data the agent can propose changes to;
  raising a cell's risk class (e.g. `virtual` → `tuya`) always needs the
  owner's approval (ADR-0014).
- Spec 1 states plainly that the product is the agent.

## Revisit when

The agent milestone overruns enough to threaten the gate; then cut the
reconfiguration milestone before cutting delivery guarantees or tests.
