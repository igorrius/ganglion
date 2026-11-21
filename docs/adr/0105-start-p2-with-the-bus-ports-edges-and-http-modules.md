# ADR-0105: Start P2 with the bus, ports, edges and HTTP modules, and allow module pages on an edge's loopback in 0.x

- **Status:** Accepted
- **Date:** 2026-11-21
- **Deciders:** Igor Artasevych
- **Spec:** [3](../spec.md#3-deliberately-not-in-0x), [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules), [10](../spec.md#10-plan-phases-and-gates), [14](../spec.md#14-not-now); [design](../design/p2-bus-and-edge-design.md)

## Context

On 2026-11-21 every condition of the P1 gate held: the node costs about $2–3 a month against $8–10
for the old setup, the reader runs on cron without a model with its versions journaled, and the
family has no complaint. Only the two weeks the gate asks for run out on 2026-11-27.

The owner sharpened the project's direction: the system builds itself by conversation, its compiled
modules can work as services (pages, APIs, collectors), they are linked like nodes in Node-RED or
n8n, and some run on other machines. P2 as planned starts with a claude CLI worker, a runner on a
Pi, MQTT, a watchdog, embeddings and a router classifier. Section 3 excludes a web UI from 0.x, and
"Not now" lists an HTTP API and pipelines.

The bus is what every part of the new direction needs, and P2 already plans NATS. The owner's own
computer is a ready edge now that the core runs on a platform.

## Options

### A. Start P2 now with M8–M11

M8 the bus in the core, M9 ports and links, M10 security and edges, M11 HTTP modules, each with its
own "done when" (design section 3). The rest of P2 follows. Module pages served on an edge's loopback
are allowed in 0.x; a web UI of the core stays out.

### B. Keep the order: P2 as planned, the new direction after the public release

The bus arrives with the worker and the Pi runner; HTTP modules and links wait for P3 or later.

### C. Build the new direction on the P1 node without NATS

Ports, links and HTTP modules over the SQLite inbox and an in-memory bus, NATS later.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Serves the owner's direction now | yes | late | partly, no edges |
| Real experience with NATS (KV, JetStream, security) | from M8 | later | none |
| Rework later | none planned | none | the transport twice |
| Fits the P2 gate (link loss, update on an edge) | M10 is the gate | yes | no |
| Waits for the P1 gate's two weeks | design and M8 start before 2026-11-27 | yes | no |

## Decision

A (owner, 2026-11-21). The gate's conditions hold and only its calendar is left; the bus is the
base of everything the owner wants next, and the owner's computer makes the edge real now. The P1
gate is still measured on or after 2026-11-27 and closed in its issue.

A page a module serves on an edge's `127.0.0.1` is a module's output to the person sitting at that
machine, not a UI of the node: it is allowed in 0.x. A web UI or dashboard of the core, pages
reachable from the LAN or the internet, and an HTTP API of the core stay in "Not now".

## Security

This ADR changes the plan; the boundaries it opens are decided in ADR-0106 to ADR-0110: the bus
(0106), cells in KV (0107), links between modules (0108), edges joining the network (0109) and a
module answering HTTP (0110).

## Consequences

- P2's remaining items (worker, MQTT, watchdog levels, embeddings, classifier, runner policies)
  follow M11.
- Section 3 and "Not now" change: the pipelines item is taken in by M9, `notify` routing on a bus by
  M8, pages on an edge's loopback by M11.
- The order puts security (M10) before the first page (M11), since the page lives on an edge.

## Revisit when

The P1 gate fails its final reading on 2026-11-27, or M8's memory measurement shows the core cannot
carry the embedded server on its host.
