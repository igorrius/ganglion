# ADR-0106: Embed nats-server with JetStream as the bus, allow an external server or cluster, and reach it only through narrow interfaces

- **Status:** Accepted
- **Date:** 2026-11-21
- **Deciders:** Igor Artasevych
- **Spec:** [5.2](../spec.md#52-deployment-modes), [5.3](../spec.md#53-components), [9.1](../spec.md#91-core--edge-protocol-p2), [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules) (R-BUS-1, R-BUS-2, R-BUS-7); builds on [ADR-0013](0013-protocol-envelope-around-stateless-mcp.md); [design](../design/p2-bus-and-edge-design.md) sections 4–5

## Context

Spec 5.2 allowed P1 an in-memory bus "behind the same interface, or embedded NATS inside the process
only", and planned NATS with JetStream for P2. ADR-0105 starts P2 with the bus. The owner wants real
NATS from the first milestone (KV, JetStream, its security model, metrics), and the freedom to run the
server apart from the node: on a small host, to restart the core while edges keep sending, or inside
an existing NATS installation. The owner also asked that the code not be tied to NATS itself, should
its licence or stewardship ever change, as they feared it might in 2025.

## Options

### A. Embedded server by default, an external one as an option, all code through `nats.go` behind narrow interfaces

The node starts `nats-server` in its process with JetStream on disk, or connects to `bus.urls`. The
code that uses the bus sees only small interfaces declared by its consumers (a cell store, a notice
publisher, an event consumer, a call responder); `bus/natsbus` alone imports NATS.

### B. An in-memory bus first, NATS later

A Go bus inside the process now, NATS when edges arrive.

### C. An external NATS service only

The node always connects to a separate server.

### D. Another broker (MQTT, Redis Streams, a home-made bus over SQLite)

## Comparison

| Criterion | A | B | C | D |
|---|---|---|---|---|
| Works out of the box on one host | yes | yes | no, a second service | depends |
| KV, streams, object store, leaf nodes, JWT auth in one piece | yes | no | yes | no |
| Experience with the real thing from the start | yes | no | yes | n/a |
| The core restarts while edges keep sending | with external | no | yes | depends |
| Replaceable if NATS becomes unusable | through the interfaces | yes | as A | n/a |
| Memory in the core's process | higher (measured in M8) | low | low | varies |

## Decision

A (owner, 2026-11-21). One binary still works alone, the external server covers small hosts and
existing installations, and the narrow interfaces keep the rest of the code free of NATS. Tests run
a real embedded server; no fake implementation is written until a second one is needed (owner).

Details:

- `bus.server: embedded` (default) or `external`; `bus.urls` (one server or a cluster);
  `bus.credentials` as `store:` or `env:`.
- The embedded server keeps JetStream in `/data/bus` (file store) and opens no TCP port in M8; M10
  adds a WebSocket listener on loopback.
- Subjects drop the draft's network token, since the account is the network: `g.notify.<module>`,
  `g.ev.<node>.<module>.<port>`, `g.rpc.<node>.<module>.<port>`, `g.tel.<node>`.
- Spec 9.1's envelope fields travel as NATS headers (`Nats-Msg-Id`, `Ganglion-From`,
  `Ganglion-Depth`, `Ganglion-Run`, trace context); a tool call between nodes still carries one MCP
  JSON-RPC request (ADR-0013).
- The platform template keeps the embedded server by default; an external one is an option.

## Security

- **Trust boundary:** the bus connects the node's own parts in M8 (in-process, no port) and, from
  M10, other nodes (ADR-0109).
- **What an attacker controls:** in M8 nothing new; with `external`, the network between the node and
  the server.
- **Abuse cases:** a client without credentials on an external server, which the server's JWT auth
  refuses; TLS verified to an external server, never skipped (conventions 11); the credentials live
  in the secret store, never in the file or a log.

## Consequences

- A new dependency: `github.com/nats-io/nats-server/v2` and `github.com/nats-io/nats.go` (both
  Apache-2.0).
- The core's memory grows; M8 measures it first, and the platform's limit may be raised.
- Backups export what lives in JetStream that is data (the cells, ADR-0107); streams are delivery and
  are not backed up.

## Revisit when

NATS's licence or stewardship changes, or the embedded server costs more memory than the host can
give.
