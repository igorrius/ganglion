# ADR-0013: Core ↔ edge protocol is our envelope over NATS carrying stateless MCP; A2A only for external agents

- **Status:** Accepted — sets the direction; message formats are fixed in P2
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** [9.1](../spec.md#91-core--edge-protocol-p2), P0 task 0.4
- **Research:** [MCP and A2A coverage](../research/2026-11-01-mcp-a2a-coverage.md)

## Context

Spec 9.1 drafts a control protocol between core and edges: an envelope with
routing, `ttl` and idempotency, message types for node lifecycle, events,
context, modules, policies and commands, and MCP for tool descriptions and
calls. Before fixing it, P0 checked what the current MCP (2026-07-28) and
A2A (1.0) already standardise.

Constraints from the spec: edges dial out and work offline; delivery is
at-least-once with idempotency; every command expires; per-node keys;
policies enforced by the executor; small devices on MQTT.

## Options

### A. Our envelope over NATS, MCP 2026-07-28 as the tool payload

As drafted in 9.1, with the MCP part as an MCP-compliant custom transport
over NATS request/reply.

### B. MCP only (Streamable HTTP or a custom binding), no envelope of our own

Extend MCP through its extensions mechanism for everything else.

### C. A2A as the core ↔ edge protocol

Edges as A2A agents; core as client; tasks for commands.

## Comparison

| Criterion | A. Envelope + MCP | B. MCP only | C. A2A |
|---|---|---|---|
| Tool discovery and calls | MCP, unchanged | MCP | Wraps tools in tasks; tools are MCP's job per A2A itself |
| Edge dials out, core still sends commands | Yes (NATS leaf) | Server never initiates requests; core would have to be the server and edges poll | Push needs an HTTP-reachable receiver |
| At-least-once, redelivery, offline queue | JetStream | Removed in 2026-07-28 ("A broken response stream loses the in-flight request") | Streams not reliable; push only |
| Command `ttl` | Ours | None | None |
| Liveness / leases | Ours | `ping` removed | None |
| Module and policy delivery | Ours | Would be custom extensions anyway | Not covered |
| Weight on an edge | Small | Small | All core A2A operations required for a custom binding |
| Interop with MCP tools and hosts | Full for tools | Full | Indirect |

## Decision

**A** (accepted 2026-11-01). MCP fits exactly the tool part and becomes simpler to carry now that
it is stateless; everything else 9.1 needs is absent from both standards in
a form an outbound-only, offline-capable edge can use. A2A is kept for a
later, separate purpose: exposing the core to external agents.

Details to carry into P2:

- Target MCP revision 2026-07-28; one NATS request carries one JSON-RPC
  request, progress notifications go to the reply subject; follow the
  custom-transport rules (JSON-RPC format, message patterns, `_meta`).
- Long-running tool calls use the MCP tasks extension rather than our own
  mechanism, once it leaves Beta.
- Extension and `_meta` identifiers use reverse DNS instead of
  `x.<author>.*` (prefix to be chosen with the project's domain).
- Unknown envelope fields are ignored; unknown message types get
  `unsupported`.
- CBOR only on the runner ↔ microcontroller MQTT hop.

### Connectivity and trust come from NATS, not from MCP or A2A

MCP's client and server are roles of messages, not network connections: the
core (MCP client) publishes `tools/call` to an edge's subject and the edge
(MCP server) answers on the reply inbox. Both sides only dial out to NATS —
the edge through its leaf node — so no inbound ports or NAT forwarding are
needed. Only MCP's Streamable HTTP transport needs direct reachability, and
we do not use it inside the mesh.

Authentication inside the mesh is NATS's: TLS, a JWT and NKey per node,
subject permissions that confine an edge to its own subjects, revocation.
MCP's OAuth applies to its HTTP transport only; for custom transports the
specification leaves authentication to the transport, so MCP OAuth is not
used between core and edges.

What NATS does not cover stays ours (spec 9.3): module signing, action
policies, the executor's `ttl` check before acting, and idempotency of side
effects. NATS encrypts each hop up to the server, and the server on the core
sees messages in clear; a compromised core could send any command. Two
defences do not depend on the core being honest: modules run only if signed
with the **owner's** key, not the core's, and policies are enforced by the
**edge that acts**. Module secrets are encrypted to the key of the node that
needs them for the same reason.

## Consequences

- Spec 9.1 now names extensions in reverse DNS.
- We own a NATS binding of MCP; it must stay compliant as MCP evolves
  (twelve-month deprecation window per MCP's lifecycle policy).
- An A2A endpoint on the core is a possible feature after P3, listed under
  "Not now".

## Revisit when

MCP adds delivery guarantees or server-initiated messaging back, or A2A
gains a binding for outbound-only peers.
