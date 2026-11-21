# ADR-0108: Declare ports in the manifest, keep links in the node's file, and carry events on JetStream and calls on request/reply

- **Status:** Accepted
- **Date:** 2026-11-21
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-11, R-RUN-14, R-RUN-15, R-RUN-20), [9.6](../spec.md#96-flow-engine-p3), [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules) (R-BUS-5, R-BUS-6); supersedes the transport of [ADR-0065](0065-cell-triggers-through-the-inbox.md) from M9; [design](../design/p2-bus-and-edge-design.md) section 6

## Context

Modules are linked only through cells today, and the trigger travels through the node's inbox
(ADR-0065, which chose the inbox as the path that "moves to NATS"). The owner wants modules wired
like nodes in Node-RED or n8n: a graph that a person can later see and change, through the agent or
by hand, without rebuilding modules. The owner also wants request/reply, which NATS offers, kept in
the design from the start.

## Options

### A. Ports in the manifest, links in the node's file

A module declares typed `emits`, `accepts`, `serves` and `calls`; `links:` in `ganglion.yaml`
connects an output to an input or a call to a service. A link is configuration the owner approves.

### B. Subscriptions in the manifest

A module names what it listens to (`listens: deye-reader.reading`), as `cells.read` does today.

## Comparison

| Criterion | A | B |
|---|---|---|
| Re-wiring needs a rebuild | no | yes |
| A graph can change links | yes | it can only show them |
| One module reused in different chains | yes | no, its sources are fixed |
| What the install window shows | the ports | the subscriptions |
| Uniform with `cells.read` | no | yes |

## Decision

A (owner, 2026-11-21): the graph edits links, not modules.

- **Ports:** `emits`, `accepts`, `serves`, `calls`, each with a shape in the manifest's shape
  language (R-RUN-14). A module with `notify` has an implicit output `<module>.notify`.
- **Links:** `{from, to}` lines in `links:`; the agent proposes one with a card (both ends, the
  shape), the owner approves; no rebuild.
- **Checks when written:** ends exist and match in kind and shape; no cycle across all links and cell
  triggers; depth at most 4.
- **Events:** the host function `emit(port, payload)` (the manifest's `event.emit` capability becomes
  real) publishes to `g.ev.<node>.<module>.<port>`; each link has its own durable consumer; a run
  acks, a failure is redelivered with back-off and counts toward the crash loop.
- **Calls:** a `serves` port is an endpoint of a NATS `micro` service named after the module; the host
  function `call(port, payload)` waits at most the run's remaining time; no responder is an error at
  once; calls are not stored.
- **Shapes** are checked when published, when received, and on a call's answer.
- **Cell triggers** move onto the bus in M9: a cell change is an event of its owner. This supersedes
  ADR-0065's transport (the inbox event `module.trigger` and the end-of-run fan-out); its rules stay:
  every listened cell is in `cells.read`, no self-listening, no loops, depth 4, values read with
  `cell_get`.
- **The journal** records `event` and `call` causes with their links; `/why` names them.

## Security

- **Trust boundary:** one module's output entering another module.
- **What an attacker controls:** a module's payload; the agent's proposed link (written under outside
  influence).
- **Abuse cases:** a payload that breaks the receiver, refused by shape checks on both sides; a loop
  or a deep chain, refused when the link is written and by the depth header; a link the owner did not
  want, which needs the owner's press like any configuration change; a module's string reaching the
  model, which taints as today (R-RUN-13); a call that hangs, bounded by the run's time.

## Consequences

- `ganglion.yaml` grows a `links:` section with its own checks and history (confighist).
- The inbox no longer carries module triggers; `module.trigger` events go.
- The graph (Not now) reads ports from manifests, links from the file and endpoints from `micro`.

## Revisit when

Links need conditions or transformations of their own; that is the `transform` module's job until
then.
