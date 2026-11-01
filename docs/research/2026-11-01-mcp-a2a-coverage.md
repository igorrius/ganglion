# What MCP and A2A already cover of the core ↔ edge protocol

- **Date:** 2026-11-01
- **Task:** P0 · 0.4 ([spec 10.1](../spec.md#101-p0--recon--tasks-and-acceptance))
- **Question:** before fixing the protocol drafted in
  [spec 9.1](../spec.md#91-core--edge-protocol-p2), which parts do MCP and
  A2A already standardise, so we do not reinvent them?
- **Outcome:** [ADR-0013](../adr/0013-protocol-envelope-around-stateless-mcp.md)
  (accepted)

## Sources

- MCP specification, revision **2026-07-28** (latest; previous 2025-11-25):
  [changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog),
  [transports](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports),
  [extensions](https://modelcontextprotocol.io/extensions/overview),
  [tasks extension](https://modelcontextprotocol.io/extensions/tasks/overview).
- A2A specification **1.0** (v1.0.0, 2026-03-12; v1.0.1 bug-fix release
  2026-05-28), Linux Foundation / Agentic AI Foundation:
  [specification](https://a2a-protocol.org/latest/specification/),
  [releases](https://github.com/a2aproject/A2A/releases).

Key claims below were checked against these pages; quotes are verbatim.

## MCP 2026-07-28 in brief

The latest revision is a large break from 2025-11-25:

- **Stateless.** "Make MCP stateless: remove the `initialize` /
  `notifications/initialized` handshake. Every request now carries its
  protocol version and client capabilities in `_meta`." Sessions and the
  `Mcp-Session-Id` header are gone; `server/discover` advertises versions and
  capabilities.
- **No redelivery.** "Remove SSE stream resumability and message redelivery
  … A broken response stream loses the in-flight request; clients MUST
  re-issue it as a new request with a new request ID."
- **No server-initiated requests.** "servers do not initiate JSON-RPC
  requests and clients do not send JSON-RPC responses." Elicitation and
  similar flows use multi round-trip requests (`resultType:
  "input_required"`). `ping` is removed; Sampling, Roots and Logging are
  deprecated.
- **Custom transports allowed.** "Clients and servers MAY implement
  additional custom transport mechanisms … MUST preserve the JSON-RPC
  message format, the message patterns, and the per-request metadata model."
  Messages "MUST be UTF-8 encoded" — JSON only, no CBOR.
- **Extensions.** Reverse-DNS identifiers (`com.example/…`) advertised in
  `capabilities.extensions`; `_meta` keys follow the same rule, with
  `io.modelcontextprotocol/` reserved. Long-running work is the
  `io.modelcontextprotocol/tasks` extension (Beta).
- **Auth.** OAuth 2.1 on HTTP, optional; machine-to-machine via the
  `oauth-client-credentials` extension. No message signing.
- **Change notifications** (`tools/list_changed` and so on) flow only on a
  `subscriptions/listen` stream with no replay after reconnect.

What it means for us: a self-contained request per message fits NATS
request/reply naturally — one NATS request carries one JSON-RPC request,
with progress notifications on the reply subject. Everything about delivery,
liveness and node management is outside MCP.

## A2A 1.0 in brief

- Built for "independent, often opaque, AI agents [to] communicate and
  collaborate with each other as peers"; MCP is for tools, and "an A2A
  Server agent may use MCP" (Appendix B).
- Agent Card discovery (optionally JWS-signed), tasks with a defined
  lifecycle (`SUBMITTED … COMPLETED, FAILED, CANCELED, REJECTED`), artifacts,
  SSE streaming, version header, URI-named extensions, "SHOULD ignore
  unrecognized fields".
- Server-to-client delivery is push over HTTP: "client must be reachable
  via HTTP". Edges that only dial out cannot receive it.
- Streams are not reliable: clients "MAY not receive all status update
  messages if the client is disconnected"; no per-request TTL.
- Custom bindings are allowed but MUST "support all operations defined in
  Section 3" — heavy for an edge node.

## Coverage of spec 9.1

| Ours | MCP 2026-07-28 | A2A 1.0 | Keep ours? |
|---|---|---|---|
| `node.hello` / `node.welcome` | Partly: `server/discover`, version and capabilities in `_meta`; no config or policy push | Partly: Agent Card, version header | Yes; tools via `server/discover` + `tools/list` |
| `node.heartbeat` | No (`ping` removed) | No | Yes |
| `tool.register` | Partly: `tools/list` schemas and hint annotations; no permissions field | No (skills are coarse) | Yes, carrying MCP `tools/list` |
| `tool.call` / `tool.result` | **Yes**: `tools/call`, progress, cancellation, tasks extension | Could wrap, but tools are MCP's job | Use MCP inside our envelope |
| `event` with idempotency key | No | Partly: `messageId` dedup is optional | Yes |
| `context.get` / `context.put` | Partly: `resources/read`; no write | No | Yes |
| `module.deploy` / `activate` / `rollback` | No | No | Yes |
| `policy.update` | No | No | Yes |
| `cmd` | No | No | Yes |
| Envelope `from`, `to`, `corr`, `ttl` | Partly: id, `traceparent`; no routing, no ttl | Partly: ids; no ttl | Yes |
| NATS / MQTT / WebSocket | Allowed as custom transport | Allowed, must implement all operations | Ours, MCP-compliant binding |
| CBOR for microcontrollers | No (UTF-8 JSON only) | No | Only between a runner and its MQTT devices; MCP stays JSON |
| Capability negotiation, unknown type | Per request; `-32601` for unknown methods | `-32601`; ignore unknown fields | Ours per node; adopt "ignore unknown fields" |
| Extension naming `x.<author>.*` | Reverse DNS | URIs with version | Switch to reverse DNS to match MCP |
| At-least-once + idempotency | No (at-most-once, client retries) | Push only | Yes (JetStream) |
| `ttl` on every command | No | No | Yes |
| Per-node keys, signed modules, executor policies | Partly: OAuth client credentials on HTTP | Partly: mTLS/OAuth, signed Agent Card | Yes (NKeys, ed25519) |
| Offline queueing | No | No | Yes |
| Long-running work and status | Tasks extension (Beta) | Task lifecycle, subscribe, cancel | Prefer MCP tasks for long tool calls |

## Conclusions

1. **MCP is the payload, not the protocol.** Tool discovery and calls use
   MCP 2026-07-28 unchanged inside our envelope, through an MCP-compliant
   NATS binding. The stateless revision makes this simpler than planned.
2. **Our envelope stays** for liveness, events, delivery guarantees, ttl,
   routing, module and policy delivery, commands and offline operation —
   none of which MCP or A2A provide in a form an outbound-only edge can use.
3. **A2A is not the control plane.** It is a candidate for exposing the core
   to *external* agents after P3; we borrow its task state names, the
   "ignore unknown fields" rule and the signed-card idea.
4. **Naming:** use reverse-DNS identifiers for our extensions and `_meta`
   keys instead of `x.<author>.*`, to match MCP.
5. **CBOR** applies only to the hop between a runner and microcontrollers on
   MQTT; the runner translates. MCP messages remain JSON.
6. **Pin the MCP revision.** Earlier revisions have `initialize`, sessions
   and server-initiated requests; our binding targets 2026-07-28 and treats
   older peers as out of scope until a real need appears.
