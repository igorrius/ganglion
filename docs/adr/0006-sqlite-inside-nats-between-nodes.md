# ADR-0006: SQLite inbox/outbox inside a node, NATS JetStream between nodes

- **Status:** Accepted
- **Date:** 2026-10-31
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery), [9.1](../spec.md#91-core--edge-protocol-p2)

## Context

Events from people and sensors must not be lost on restart or network loss,
and replies must not be duplicated more than rarely (R-DLV-1…8). Inside a
node we need "reply + session state + processed mark" in one transaction.
Between nodes (P2) we need buffering while an edge is offline, redelivery,
a registry, key-value state with watch, and an artifact store for modules.

## Options

### A. SQLite everywhere

Inbox/outbox tables in each node; own replication between nodes.

### B. NATS JetStream everywhere

Embedded NATS even inside a single node.

### C. SQLite inside a node, NATS JetStream between nodes

### D. An external broker (Redis, RabbitMQ, a separate MQTT broker)

## Comparison

| Criterion | A. SQLite only | B. NATS only | C. SQLite + NATS | D. External broker |
|---|---|---|---|---|
| Atomic "reply + state" | Yes, one transaction | No, two systems | Yes, inside the node | No |
| Offline buffering between nodes | Own code | Leaf node + JetStream | Leaf node + JetStream | Depends; usually own code |
| KV with watch, object store | Own code | Built in | Built in | Partly |
| RAM in the all-in-one MVP | Lowest | +10–30 MB | Lowest in P1, NATS added in P2 | Extra service |
| Backups | Copy one file | JetStream snapshots | One file + JetStream | Broker-specific |
| MQTT devices | Separate broker | NATS speaks MQTT | NATS speaks MQTT | Broker-specific |
| Own code to write | Hundreds of lines + replication | Little | Hundreds of lines in P1 | Integration |

## Decision

**C.** SQLite gives the single transaction the outbox pattern needs and
keeps the P1 process small. NATS pays for itself only once there are
several nodes, and then it brings offline buffering, redelivery,
deduplication (`Nats-Msg-Id`), KV with history and watch, object store,
auth, and MQTT ingestion without our own code.

## Consequences

- The `queue.Queue` interface has two implementations: SQLite (P1) and
  JetStream (P2).
- Model turns are not idempotent; side-effecting tools must honour an
  idempotency key (R-DLV-6).
- One duplicate reply is possible if the process dies between sending and
  marking; accepted for a family bot.

## Revisit when

Cross-node transactions become necessary, or embedded NATS RAM exceeds
the edge budget measured in P0.
