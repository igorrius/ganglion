# ADR-0114: An edge keeps its events in its own JetStream domain, the core's EVENTS stream sources them, and the edge's telemetry is a Prometheus snapshot that also tells the core when to resync

- **Status:** Accepted
- **Date:** 2026-11-23
- **Deciders:** Igor Artasevych
- **Spec:** R-EDGE-7, R-OBS-9, [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules); builds on [ADR-0106](0106-embed-nats-with-jetstream-as-the-bus-behind-narrow-interfaces.md), [ADR-0109](0109-a-trust-chain-in-the-vault-and-edges-joined-by-a-one-time-code.md), [ADR-0113](0113-the-embedded-bus-runs-in-operator-mode-and-adopts-its-store-into-the-network-account.md); [design](../design/p2-bus-and-edge-design.md) sections 7.6 and 7.8

## Context

R-EDGE-7 asks for an edge that keeps its modules running and its events
kept while its link to the core is down, and that the core gets those
events when the link returns. R-OBS-9 asks for a metrics snapshot every
`edge.telemetry_every` on `g.tel.<edge>` over plain NATS, and the spec asks
for a `/status` line per edge. The design (7.6) names the mechanism in one
sentence: a leaf server with its own JetStream domain, whose `EVENTS`
stream the core's stream sources. Building it leaves these questions:

1. How the edge's events travel: only through sourcing, or also as plain
   messages over the leaf link.
2. How the core learns which edges to source, before the join of #322
   keeps a list.
3. How soon the copy resumes after the link returns. Measured on
   nats-server 2.15.3: a source that failed while the link was down is
   retried with a back-off from 5 s up to 2 minutes; in a test the first
   copy after a restart of the core came 38 s after the link was up.
4. What the snapshot holds and how the core trusts it.
5. What the edge's user must be allowed, so the join (#322) can issue it.

## Options

### Event path

- **A1.** Sourcing only: the edge's leaf remote denies exports of `g.ev.>`,
  so an event reaches the core only as a copy of the edge's stream.
- **A2.** Both: the event goes over the link as a plain message when the
  link is up (the core's stream captures it) and is sourced as well; the
  stream's duplicate window drops the second copy.
- **A3.** No edge stream: the edge publishes to the core's stream through
  the hub's JetStream API and buffers in memory or SQLite while down.

### Which edges are sourced

- **B1.** The core sources an edge the first time it hears its telemetry;
  only a joined edge can publish `g.tel.<edge>` (R-EDGE-2), and the name
  comes from the subject.
- **B2.** A list in the settings file (`edges:`) the owner keeps.
- **B3.** Wait for #322's registry of joined edges.

### Resuming after the link returns

- **C1.** Leave it to the server's retry (up to 2 minutes).
- **C2.** The edge sends a snapshot as soon as its link comes up; the core,
  hearing an edge after a silence, removes and adds its source, which the
  server starts again at once from the last copied message.

### Snapshot

- **D1.** JSON: link state, events held, module versions, and the node's
  metrics in the Prometheus text format, parsed and relabelled by the core.
- **D2.** OTLP metrics over NATS.

## Comparison

| Criterion | A1 | A2 | A3 |
|---|---|---|---|
| A copy stored twice | never | when the second copy comes later than the 2-minute window (a slow source after a long outage) | never |
| Order across an outage | the edge's order | live events before older kept ones | the edge's order |
| Moving parts | one deny | none | a buffer of our own beside JetStream |

| Criterion | B1 | B2 | B3 |
|---|---|---|---|
| Works before #322 | yes | yes | no |
| Owner's work | none | edit the file per edge | none |
| A stranger becoming a source | needs a NET user JWT, issued only on the owner's approval | no | no |

| Criterion | C1 | C2 |
|---|---|---|
| Delay after the link returns | up to 2 minutes | about one second |
| Risk | none | a remove and add of the source; measured: no message is copied twice or lost, without the help of the duplicate window |

| Criterion | D1 | D2 |
|---|---|---|
| New dependency | none (`prometheus/common` is in the module graph already) | the OTLP protobuf types on the edge and the core |
| Fits the core's own `/metrics` | directly | needs a translation |

## Decision

A1, B1, C2, D1.

- **Domain:** an edge's embedded server runs with
  `JetStreamDomain = <edge>` and one leaf remote to the core; its streams
  have the core's names (`EVENTS`, `NOTIFY`, the `cells` bucket), capped at
  256 MiB each with the oldest message dropped first, besides the 7-day age.
  The edge's server has no trust chain of its own: one in-process user,
  the edge, and no listener.
- **Event path:** the leaf remote denies exports of `g.ev.>`; the core's
  `EVENTS` stream sources `EVENTS` in the domain through the API prefix
  `$JS.<edge>.API`. `Ensure` keeps the stream's sources when it brings the
  stream to its settings.
- **Which edges:** the core's telemetry collector sources an edge the first
  time it hears it; `UnsourceEdge` is there for revocation (#322).
- **Resume:** the edge sends a snapshot as soon as its link comes up; when
  the core hears an edge for the first time since its start, or after more
  than three intervals of silence, it resyncs the source.
- **Snapshot:** JSON on `g.tel.<edge>` with the version (`v: 1`), the
  time, whether the link is up, the events the edge holds, its modules and
  their versions, and its metrics in the Prometheus text format. The core
  takes the name from the subject, refuses a payload over 256 KiB, keeps at
  most 64 edges and 4000 series per edge, and serves the edges' series on
  its own `/metrics` with a `node` label.
- **The edge's user (for #322):** measured with nats-server 2.15.3, the
  sourcing needs the edge to publish `$JS.S.>` (the copies) and
  `$JSC.R.>` (answers to the core's requests), and to subscribe to
  `$JS.<edge>.API.>`; telemetry adds publish `g.tel.<edge>`.

## Security

- **Trust boundaries:** the edge to the core over the leaf link; the
  snapshot's content to the core's `/metrics` and `/status`.
- **What an attacker controls:** a compromised edge: what it publishes
  under its own subjects, its snapshot, its stream's content.
- **Abuse cases and what stops it:**
  - an edge naming another edge in its snapshot: the name is taken from the
    subject, which its permissions bind to its own name;
  - a huge snapshot or a flood of series: the size and series caps; what is
    over them is dropped and counted, never stored;
  - labels or names that are not Prometheus names in the text: the parser
    refuses the snapshot; the `node` label from the payload is replaced;
  - an edge publishing `g.ev.>` directly to bypass its stream: denied on
    the edge's side, and the edge's user cannot publish `g.ev.>` at the
    core (R-EDGE-2);
  - an edge flooding its own stream: the 256 MiB cap is on the edge; the
    core's stream keeps its own limits;
  - module text in `/status`: the line shows module names and versions from
    the snapshot as values, never as markup.

## Consequences

- A lost link loses no event that fits the edge's caps; the core has the
  events about a second after the link returns.
- A new edge appears in `/status` with its first snapshot, before #322
  keeps a list; #322 adds `UnsourceEdge` to revocation and the permissions
  above to the edge's user.
- The core's `/metrics` grows by every edge's series.
- Notices of modules on an edge stay in the edge's `NOTIFY` stream until
  the core delivers them by the node's file (#323).

## Revisit when

nats-server shortens its source retry or gives a callback for a leaf
connection, or an edge needs more than one link (two cores).
