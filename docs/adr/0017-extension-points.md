# ADR-0017: Map the extension points: the core keeps guarantees and trust, everything else is contributed

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** principle 4, [7.5](../spec.md#75-runner-and-wasm-modules), [7.6](../spec.md#76-action-policies-and-risk-classes), [9.8](../spec.md#98-self-modification-levels), [14](../spec.md#14-not-now)

## Context

ganglion is a modular system: the agent grows it with configuration,
skills, scripts, MCP modules and WASM modules (spec 9.8), and never rebuilds
the core ([ADR-0008](0008-agent-never-rebuilds-core.md)). ADR-0008 says the
core "must expose enough extension points", but no document says which
parts of the system can be extended, by which level, and what the core must
provide for that.

The question came up with Telegram: should the bot's features (commands,
menus, buttons, later web pages inside Telegram) be built into the
transport, or contributed by modules? The same question will come up for
every part of the system, so it is answered once, as a map and a rule.

Constraints:

- The module ABI is call-based: a fresh instance per call, bounded by a
  timeout, side effects only through host functions
  ([ADR-0011](0011-module-abi.md), R-RUN-1, R-RUN-2). A module cannot hold a
  socket or a long-lived connection.
- Policies are enforced by the node that executes the action (R-POL-1); a
  human confirmation must not be forgeable by the code that asked for it.
- Minimal memory (principle 8): each extension mechanism costs host memory.
- Delivery guarantees (R-DLV-*) must hold no matter what is plugged in.

## Options

### A. Decide case by case

Each feature decides when it is built whether it is core code or an
extension. No shared rule.

### B. A map of extension points with one rule

The core owns guarantees and trust. Every other part of the system is an
extension point with a declared contract; contributions come through one of
the existing levels (0–4 in spec 9.8). A short rule decides whether a
contribution can be a WASM module, needs an MCP module (a process), or is
core code.

### C. Microkernel: everything is a module

Transports, providers and cell backends become modules too; the core is only
the runner, the bus and the queue.

## Comparison

| Criterion | A. Case by case | B. Map and rule | C. Microkernel |
|---|---|---|---|
| Consistency for the agent (P3) | Every part extends differently | One set of contracts the agent can learn | One contract |
| Trust boundaries | Decided per feature; easy to leak (e.g. a module drawing a confirmation button) | Fixed: listed as core-only | Trust logic moves into modules, which the agent can rewrite |
| Delivery guarantees | Depend on each feature | Stay in the core | Depend on transport modules |
| Fit with the ABI | — | Uses the ABI as it is | Needs long-lived, streaming host interfaces: a new ABI |
| Memory | — | Mechanisms that already exist | An instance per transport and provider, always resident |
| Effort now | None | A document; one boundary rule in M2 | A rewrite of P1 |

## Decision

**B.** It costs a document now and keeps the trust and delivery code out of
anything the agent can change. C fails on the ABI and on trust: the code
that decides who is speaking and whether an action was confirmed would
become rewritable by the agent.

### What the core always keeps

Not extensible, at any level:

- delivery: inbox, outbox, leases, idempotency (R-DLV-*);
- identity and access: the `allowed` table and roles; which person a
  message comes from;
- policy enforcement, risk classes, kill switches, human confirmations and
  their rendering (7.6);
- secrets, module signing and verification, audit;
- the runner and its ABI, the router, history and conversation actors;
- channel wire protocols and their credentials (a bot token never leaves
  the transport).

### What can be a WASM module

A contribution can be a WASM module when all of these hold:

1. it is a bounded call: input in, output out, within its timeout;
2. it holds no socket or connection of its own (`http.fetch` per request is
   fine);
3. its side effects go only through granted host functions;
4. it establishes no trust: it does not decide who a person is, does not
   confirm actions and holds no secret beyond those granted to it.

Fails 1 or 2 → an MCP module (a separate process with a manifest,
level 3). Fails 4 → core code, written by people.

### The map

| Part | The core owns | Contributions | Level | Phase |
|---|---|---|---|---|
| Sensors and data readers | Sensor cells, scheduler | Reader modules (`deye-reader`) | 4 | P1 |
| Reflexes and flows | Runner, supervisor, policies, kill switches | Modules; flows built from blocks | 4; flows P3 | P1, P3 |
| Agent tools | Tool registry, policy check, idempotency keys, audit | Skills, scripts, MCP modules, module tools (`triggers: tool:`) | 1–4 | P1–P3 |
| Devices | Cell model, reconcile, history, risk class on the binding (ADR-0014) | Backends: Go in the core today (`virtual`); device backends as modules through a generic module backend, when the first real device needs it | 0, 3, 4 | P1–P2 |
| Notifications | Observer interface, outbox | Observer bindings in config; observers as modules | 0, 4 | P1 (config) |
| Policies | Enforcement, confirmations; risk class set by a human | Declarative rules; the agent may propose, not lower a class | 0 | P1 |
| Memory | Storage, git, index | Facts files, the consolidation prompt, skills | 1 | P1 |
| Model providers | Provider interface, history format, router | Endpoints of a known API family in config; a new API family is core code | 0 | P1 |
| Workers | Worker protocol | Separate processes (claude CLI, Ollama) | — | P2 |
| Chat channel features | Wire protocol, polling, offset, 429 and 409, access, rendering, confirmations | Commands, menus and buttons declared by tools and modules (below) | 0–4 | P3 |
| New chat channels | — | Core code: a channel holds a connection and establishes who is speaking | — | Not now |

### Chat channels: core transport, contributed features

The transport is core code; the bot's features are contributed. A tool or
module declares, in its manifest, channel-neutral UI: commands with a
description and the role allowed to use them, and buttons on its messages.
The transport renders them for its channel (for Telegram: `setMyCommands`
per role, inline keyboards) and degrades what a channel cannot show.

- A button press is a `tool.call` from the person who pressed it, through
  the same policies and confirmations as a call from the model.
- Button payloads are opaque ids that the core issues and stores; a module
  never writes raw `callback_data` (Telegram limits it to 64 bytes anyway).
- Confirmation buttons are a core-only kind, rendered distinctly; no module
  can produce one.
- Channel types (Telegram updates, keyboards) do not leave the transport
  package; the rest of the system sees `event.Event` and `event.Outgoing`.

Web pages inside the chat (Telegram Mini Apps) would need an HTTP server and
validation of Telegram's signed `initData` in the core — a new security
boundary — and are a web UI, which 0.x excludes (spec 3). Not decided; on
the "Not now" list.

## Consequences

- Each new part of the system is placed on the map first: what the core
  owns, what can be contributed, at which level. A part that does not fit
  the map is a reason for a new ADR.
- Each extension point has a contract and a conformance suite, as module
  hosts (`runner/runnertest`) and cell backends (`cells/cellstest`) do.
- M2 keeps Telegram types inside the transport and `Outgoing`
  channel-neutral; new fields (buttons, silent, topic) are added as neutral
  fields. Nothing else changes in P1.
- Channel UI contributions, a generic module cell backend and Mini Apps are
  future work, each with its own ADR when it starts.
- We give up the flexibility of C: a new channel or API family needs people
  writing core code.

## Revisit when

A needed class of features cannot be expressed on this map, or the ABI
gains long-lived or streaming host interfaces (which would make module
transports possible).
