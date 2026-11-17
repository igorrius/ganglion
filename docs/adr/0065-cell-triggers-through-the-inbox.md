# ADR-0065: Start a module when cells it listens to change, with the event carried by the node's inbox

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-11, R-RUN-12, R-RUN-15), [7.1](../spec.md#71-guaranteed-delivery), [9.6](../spec.md#96-flow-engine-p3); [M4c design](../design/p1-m4c-reflexes-design.md) section 4; builds on [ADR-0014](0014-state-cells.md), [ADR-0062](0062-cells-owned-by-modules.md)

## Context

A module runs on cron or as the agent's tool (R-RUN-12). M4c needs a
module that reacts to another module's data: `deye-reader` writes the
battery's cells every 30 minutes, and `deye-alerts` must decide, without a
model, whether to tell the owner. This is the first link of the chains of
modules the owner wants the model to build (spec 9.6, M4c design 1).

Facts:

- Cells already have a watch in memory (`cells.Watch`).
- The node's inbox is durable, deduplicates by an idempotency key and
  carries messages and module escalations; the spec moves it to NATS
  JetStream in P2.
- A module runs one instance at a time; its runs are journaled and
  supervised.

## Options

### A. Cell trigger, delivered through the inbox

`triggers: - cell: [deye-reader.soc, …]`. When a run that wrote cells
ends, the host puts one `module.trigger` event per subscriber into the
inbox, keyed by subscriber, producer and run. The node routes it to the
module host, which runs the subscriber.

### B. Cell trigger on the in-memory watch

The same manifest; the module host subscribes to `cells.Watch` and runs
subscribers directly.

### C. A separate `bus` package with publish/subscribe

A new interface with an in-memory implementation now and NATS later.

### D. No new trigger: the subscriber has its own cron

`deye-alerts` reads the cells every 30 minutes on its own schedule.

## Comparison

| Criterion | A | B | C | D |
|---|---|---|---|---|
| Survives a node restart between write and run | Yes | No | No | Not needed |
| Duplicates | Cut by the key | Possible on re-run | Possible | None |
| Delay after the data | Seconds | Seconds | Seconds | Up to one interval |
| The link between modules is explicit | Yes | Yes | Yes | No: two schedules |
| Path to NATS | The inbox moves | Rewrite | The bus moves | — |
| New runtime parts | A kind and a route | A subscriber | A package | None |

## Decision

**A** (owner, 2026-11-10). The inbox already is the durable,
deduplicating path the spec moves to NATS, so cell events take the same
road and change transport with it (owner: "when NATS arrives we use it the
same way"). D leaves the chain implicit and late; B and C lose an event to
a restart and add a second path beside the inbox.

Details (M4c design 4):

- every cell a trigger names is in `cells.read`; a module never listens
  to its own cells; the install refuses a loop between modules;
- one event per subscriber per producer run, whatever number of cells it
  wrote; the subscriber reads values with `cell_get`;
- the chain depth travels in the event; above 4 no event is put;
- runs from triggers are unattended runs: supervised, journaled and
  escalated like cron.

## Security

- **Boundary:** one module's output starts another module's code. No new
  trust boundary for data: the subscriber could already read those cells
  through `cells.read`, which the owner approved in its window.
- **What an attacker controls:** a producer's cell values, which may come
  from a service outside.
  - A storm of writes running a subscriber without end → one event per
    producer run, one run at a time, the producer's own schedule and the
    crash loop bound it.
  - A loop `a → b → a` → refused at install; the depth cap of 4 stops it
    if one slips through.
  - A value shaped to reach the model → the trigger never involves the
    model; values keep their contracts (R-RUN-14).

## Consequences

- Chains are explicit data, visible on both modules' cards and ready to
  be drawn as a graph.
- The node's inbox gets a kind no model reads; its router must send it to
  the module host.
- An event is lost if the node dies between the producer's run and the
  put; a reflex's own cron check covers a producer that went quiet.

## Revisit when

NATS replaces the inbox (P2), or a chain needs fan-in rules (run only when
all of several cells changed), which belong to the flow engine (9.6).
