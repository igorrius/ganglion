# ganglion — project specification

Status: draft v0.1, 2026-11-01. Versions are 0.x; the API is unstable.
Source: the product and architecture decision and R&D plan of 2026-10-31
(a private design document). This file distills it for implementation: what
we build, from what, under which rules and in what order. When a decision
changes, update this file first, then the code. The architecture views are
in [architecture/](architecture/README.md), the reasoning behind decisions
in [adr/](adr/README.md).

How to read it:

- **P0–P1** are detailed: requirements and acceptance criteria. This is the
  work at hand.
- **P2–P3** are outlined: direction, invariants, draft formats. They get
  detailed once the previous phase's gate is passed.
- Requirements are tagged `R-<area>-<n>` (for example `R-DLV-3`) so tasks,
  tests and commits can refer to them.
- "Must" and "must not" are mandatory; "may" is allowed but optional.

## 1. What we are building

ganglion is an **agent**: a model-driven assistant that talks to people,
remembers, and designs and maintains its own automations. It is open source,
written in Go, for self-hosted and edge/IoT setups. Everything else in this
spec is the agent's body:

| Part | Role in the agent | Where |
|---|---|---|
| Brain | Agent loop, router, models, memory, skills | Core and workers |
| Nervous system | Guaranteed delivery: inbox/outbox, later NATS between nodes | All roles |
| Senses and hands | State cells: sensors, switches, confirmation requests (ADR-0014) | Core and edges |
| Reflexes | WASM modules that run without the model and escalate exceptions | Edge runners |

The name means a nerve node: reflexes are handled locally, without the
"brain" — but the brain designs them, watches them and changes them.

- **Core** holds conversation history, memory, the model router and
  guaranteed delivery.
- **Edge nodes** run tools and WASM micro-modules next to devices.
- **Workers** run models where the resources are.

The main idea: the model "compiles" regular tasks into small WASM modules
(reflexes) that then run on their own. The model is called only on
exceptions. The agent embeds itself into the infrastructure of someone who
runs a home server, a Raspberry Pi or a smart home, and builds automations
there itself. The basic assistant features — conversation, memory,
reminders — exist too and use the same mechanisms: chat is one transport,
skills are skills and modules, reminders are flows.

**Core hypothesis:** the agent can hand a regular task to a reflex — a WASM
module that runs without a model and calls the model only on exceptions —
and keep improving the system on request. Tested in P1 on the real Deye
battery monitor and on configuration changes the agent proposes and the
owner approves; in P3 the agent writes the modules itself.

**First user** — the author: a family Telegram bot and Deye inverter
monitoring with load shedding.

### 1.1. How it differs from neighbours

The "lightweight assistant in Go for boards" niche is taken
([PicoClaw](https://github.com/sipeed/picoclaw)). ganglion does not try to
repeat it. The differences:

1. **Distribution.** A light edge node sits next to devices; heavy model
   executors live where there is memory or a GPU.
2. **Model routing.** The provider and model are chosen by the skill's
   requirements, budget and availability, with fallback to the next option.
3. **Guaranteed delivery.** An event from a sensor or a person is not lost on
   restart or network loss.
4. **Action policies.** Opening a lock is not the same as replying in chat:
   autonomous, with confirmation, or forbidden; rate limits; audit.
5. **MCP both ways.** A node is both client and server: a device exposes its
   capabilities to other agents.

We build from scratch. Neighbours are a source of ideas, not a base.

## 2. Principles

1. **The core does not think.** It delivers, routes and executes; the model
   makes decisions.
2. **Conversation history belongs to the core, not the provider.** Otherwise
   routing between providers breaks the context.
3. **API key or local model by default.** Claude CLI on a subscription is an
   option for the subscription owner; the project never touches its tokens
   (section 7.4).
4. **The core is immutable to the agent.** The agent extends the system with
   skills and modules; all state lives in git and can be rolled back.
5. **Code is packages with clean interfaces** (`Transport`, `Queue`,
   `Provider`, `Router`, `Store`), so a node can be embedded as a library.
6. **Policies are enforced by whoever executes the action,** not by whoever
   decided on it.
7. **Build little where something ready exists.** Chat, providers and
   channels stay minimal; the effort goes into the tool pool and skill
   compilation.
8. **Minimal memory is a design goal.** The core and edge roles must run
   comfortably on the cheapest hosts: a Raspberry Pi or a small container of
   about 128 MB. Workers that host heavy model runtimes (claude CLI, Ollama)
   are exempt: they run where the resources are (ADR-0009). Memory
   is measured (`tools/modbench`) and weighed in every decision about
   dependencies, runtimes and module languages; a Pi is one target among
   others, not the reference.
9. **Secure by design, and every link is hostile** (owner, 2026-11-03).
   - **Every network is open and watched.** That includes the home LAN and
     a private cloud network.
   - **Every input is untrusted data until checked.** That covers every
     message, page, file, model output, module result and another node's
     event.
   - **What follows for links:**
     - every link is encrypted and both ends authenticate;
     - the receiver authorises each message by who sent it (principle 6);
     - each message carries a `ttl` and an idempotency key and has a size
       limit;
     - nothing listens beyond loopback unless a requirement says so.
   - **Rights** start at none and widen only by the owner's decision.
   - **Every design records its trust boundaries and abuse cases before
     code:** the ADR template's Security section,
     [conventions](conventions.md) section 11. What stops an abuse is
     code, not a prompt.

## 3. Deliberately NOT in 0.x

- a web UI or dashboard;
- channels other than Telegram and MQTT;
- more than three providers: one cloud provider with an OpenAI-compatible
  API, Ollama, claude CLI;
- a classifier in the router — rules only (until P2);
- multi-tenancy or a cloud service;
- the agent rebuilding the core (never, section 9.8);
- a stable API.

Anything that comes up along the way and is not in the current phase goes to
the "Not now" list (section 14); the phase does not grow.

## 4. Glossary

| Term | Meaning |
|---|---|
| Core | Role: history, memory, router, tool registry, inbox/outbox |
| Edge | Role: a runner next to devices — tools, sensors, WASM modules, local policies, scheduler |
| Worker | Role: model execution (API provider, Ollama, claude CLI) |
| Runner | The module runtime inside the edge role: wazero + embedded NATS + supervisor + policies + scheduler |
| Module (reflex) | A signed WASM artifact with a manifest; sees only the host functions granted to it |
| Flow | A state machine built from reusable blocks (modules and tools): sensor → rules → action → notification |
| Skill | A Markdown instruction (`SKILL.md`) with frontmatter declaring model requirements; may carry a script |
| Kill switch | A KV flag that disables actions of a flow or a device; when off, only notifications are sent |
| Risk class | Informational / comfort / physical safety; defines which confirmations are required |
| Shadow mode | A flow runs on real data but instead of acting writes "would have done X" |
| `idemKey` | Idempotency key of an event or call |
| `ttl` | Command lifetime; an expired command is not executed |
| Escalation | An event from a module to the agent: "a model is needed" |
| Tainted session | A session that external content (a web page, a search result, a forwarded message, a quoted message or a reply to a message in another chat) has entered; side effects in it need confirmation (R-POL-6) |
| Grant | What a scheduled agent task may do, fixed and approved by the owner when the task is created (R-POL-8) |

## 5. Architecture

### 5.1. Roles, not machines

One Go binary; roles are enabled with `ganglion run --roles core,edge,worker`. A single
Raspberry Pi can carry all three. A distributed setup puts the core on a
server, edges next to devices, and workers where the resources are.

```
 Telegram ─┐                         ┌─ Worker: API provider (OpenAI-compat.)
 MQTT ─────┤        ┌──────────┐     ├─ Worker: Ollama
 Cron ─────┼──────▶ │   CORE   │ ◀──▶├─ Worker: claude CLI (owner's subscription)
           │        │ inbox    │     │
           │        │ history  │   NATS (leaf + JetStream)
           │        │ memory   │     │
           │        │ router   │ ◀──▶┼─ EDGE: runner (wazero, policies, cron)
           │        │ registry │     │      └─ WASM modules ─▶ Deye, Tuya, ...
           │        │ outbox ──┼──▶  └─ EDGE: runner ─▶ MQTT devices (ESP32)
           │        └──────────┘
```

### 5.2. Deployment modes

| Mode | What runs where | Bus | Phase |
|---|---|---|---|
| All-in-one | One process: core + runner + providers | In-memory bus behind the same interface, or embedded NATS inside the process only | P1 |
| Core + edges | Core on a server, runners on Pis | Core runs a NATS server with JetStream; an edge runs a leaf node that dials the core itself (no inbound ports or NAT forwarding; WebSocket mode available) | P2 |
| Devices without a runner | ESP32, sensors, relays | MQTT to the nearest runner: NATS accepts MQTT natively, no separate broker | P2 |

There is no separate local plugin mode: local is the same runner inside the
core process.

### 5.3. Components

| Component | Role | Responsible for | Technology | Phase |
|---|---|---|---|---|
| Transports | core (chats), edge (devices) | Telegram, MQTT, later HTTP and Home Assistant → a single `Event` with `idemKey` | long polling, MQTT, net/http | P1 (TG), P2 (MQTT) |
| Inbox / outbox | core | Never lose or duplicate an event or a reply | SQLite (modernc.org/sqlite, WAL) | P1 |
| Conversation actors | core | One turn at a time per conversation, timeouts, cancellation | a goroutine per conversation | P1 |
| Conversation history | core | Provider-independent transcript, compaction of long context | SQLite + summary by a cheap model | P1 |
| Agent loop | core | Model → tool calls → next step, with a step limit | own, in Go | P1 |
| Router | core | Choice of model and tool execution location, fallback chain | rules from `SKILL.md` + node registry | P1 (rules) |
| Model providers | core or worker | OpenAI-compatible (incl. Ollama), later Anthropic Messages; translating MCP tools to the API's format | net/http | P1 (one API) |
| claude CLI worker | worker | Tasks on the owner's subscription: write a module, analyse logs | unmodified `claude -p` | P2 (ADR-0015) |
| Tools and MCP | core, edge | `memory.*`, `schedule.*`, `message.send`, `cells.*`, `web.*` in the core; `device.*` on the edge; the same tools served to CLI providers over an MCP bridge (R-PRV-6) | MCP stdio / HTTP | P1 |
| Web reading | core | `web.fetch` and `web.search` (7.11); HTML turned into Markdown by the `html2md` WASM module | net/http + a WASM module | P1 (M3b) |
| WASM runtime | edge | Running modules with host functions from the manifest; install, update and rollback (R-RUN-8) | wazero, own ABI (ADR-0011) | P0 (spike), P1 |
| Action policies | core defines, edge enforces | Autonomous / confirm / forbid, limits, `ttl`, audit; tainted sessions, clean-context checks, grants (R-POL-6–8) | declarative config | P1 (base), P2 |
| Scheduler | core, edge | Crons, catch-up after downtime; reminders and scheduled agent tasks (7.10); on the edge, modules run without a link | `cron_runs` table | P1 (M3b; module triggers M4) |
| Memory | core | Facts, skills, semantic search; slices for edges | markdown in git; embeddings | P1 (files), P2 |
| Core ↔ edge protocol | all | Registration, calls, events, modules, policies, context | envelope + MCP over NATS | P2 |
| Bus | all | Delivery, buffering during outages, module object store | NATS: leaf node + JetStream | P2 |
| Observability | all | Turn trace: input, route, tools, cost, reply; metrics; traces; the tool-call journal and logs on disk | slog + `turns` and `tool_calls` tables + OpenTelemetry (Prometheus `/metrics` with the promtop dashboard, OTLP traces) | P1 |
| Diagnostics | core | `ganglion mcp`: read-only MCP tools over logs, the journal and the config, with no running node or model needed (R-OBS-7) | MCP over stdio | P1 (M3c) |

## 6. Key interfaces (draft)

A sketch to pin package boundaries. Signatures are refined in P1 and may
change freely while on 0.x.

```go
// Event is the single input from any transport.
type Event struct {
    ID      string          // ULID
    IdemKey string          // TG: update_id; cron: job + scheduled time
    Source  string          // "telegram", "mqtt", "cron", "module:<name>"
    Conv    string          // conversation / actor key
    Kind    string          // "message", "sensor", "module.exception", ...
    Payload json.RawMessage
    TTL     time.Duration   // 0 means no expiry
    At      time.Time
}

type Transport interface {
    Name() string
    Run(ctx context.Context, sink func(context.Context, Event) error) error
    Send(ctx context.Context, msg Outgoing) error
}

// Queue is the inbox/outbox. SQLite inside a node, JetStream between nodes.
type Queue interface {
    Put(ctx context.Context, e Event) error                    // duplicate IdemKey is a no-op, not an error
    Lease(ctx context.Context, d time.Duration) (Lease, error)  // take an event under a lease
    Complete(ctx context.Context, l Lease, out []Outgoing, state StateUpdate) error // one transaction
}

type Provider interface {
    Name() string
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
} // capabilities and prices are per model, in the router's catalogue (ADR-0020)

type Router interface {
    // Ordered list of "provider + model" candidates.
    Route(ctx context.Context, task TaskReq) ([]Candidate, error)
}

type Store interface { /* history, turns, tool_calls, cron_runs, allowed, module KV */ }
```

## 7. P1 subsystems: requirements

### 7.1. Guaranteed delivery

Guarantee: **at-least-once + idempotency.**

- **R-DLV-1.** An incoming event is first written to `inbox` with a unique
  `idemKey`; duplicates are rejected by a unique index.
- **R-DLV-2.** The Telegram offset advances only after the write to `inbox`.
- **R-DLV-3.** An actor takes an event under a lease (10 min by default). If
  the process dies, the event is processed again: a restarted core
  releases the leases left in its database at start, and otherwise the
  lease expires. One core process owns a database file; a lock file stops
  a second one ([ADR-0019](adr/0019-release-leases-at-core-start.md)).
  A run a restart cut short is not a failed attempt: only an event that
  more than 10 restarts cut short is given up on, so one that kills the
  process cannot do so forever
  ([ADR-0046](adr/0046-a-restart-is-not-a-failed-attempt.md)).
  Commits are fsynced (`synchronous=FULL`), so an event confirmed to the
  channel survives a power loss.
- **R-DLV-4.** The agent's reply, the new session state and the "incoming
  event processed" mark are written in one transaction (outbox pattern).
- **R-DLV-5.** A separate sender delivers the outbox with retries, honours
  Telegram's `429 retry_after`, and marks a message sent after success.
  Accepted residue: one duplicate if the process dies between sending and
  marking.
- **R-DLV-6.** A model turn is not idempotent, so side-effecting tools get an
  idempotency key `turn_id + call number` and must honour it.
- **R-DLV-7.** Inside a node `Queue` is backed by SQLite; between nodes (P2)
  by NATS JetStream (deduplication via `Nats-Msg-Id`).
- **R-DLV-8.** Two processes must not poll with the same Telegram token at
  once: zero overlap on deploy, or switch to a webhook.
- **R-DLV-9.** A reply may carry attachments (a picture, a document) in a
  channel-neutral form; they go through the outbox with the same delivery
  guarantee as text. The Telegram transport sends them with `sendPhoto`
  and `sendDocument` (M3c). A message carries at most one attachment of
  at most 4 MiB, refused before the outbox; a text longer than the
  channel's caption (1,024 characters on Telegram) follows the attachment
  as its own parts. Bytes are kept in the outbox row
  ([ADR-0045](adr/0045-metrics-in-the-chat-and-attachments-in-the-outbox.md)).
- **R-DLV-10.** A file or a picture sent to the agent is never dropped
  silently. Until the agent can read them, the transport tells it that a
  file came (name, type, size) beside the caption, and the agent says it
  cannot open it yet (M3c; reading files is in "Not now").

### 7.2. Conversations, history and the agent loop

- **R-AGT-1.** One actor (goroutine) per conversation: one turn at a time,
  timeout, cancellation.
- **R-AGT-2.** History is stored in the core in a neutral format, not in any
  provider's format; long context is compacted into a summary by a cheap
  model.
- **R-AGT-3.** Agent loop: model call → tool calls → next step; a per-turn
  step limit.
- **R-AGT-4.** A depth limit on "the agent called itself" chains, a daily
  limit on turns and tokens; exceeding it alerts the administrator.
- **R-AGT-5.** Every turn is written to the `turns` table: input, route, tool
  calls, cost, reply (a trace for observability and memory consolidation).
- **R-AGT-6.** A conversation is split into sessions. A new session starts
  on `/new` (or `/reset`) and on the first message after 6 hours of
  silence (configurable). Ended sessions are archived with their summary
  and messages. `/sessions` lists the last five, and `/resume <n>` brings
  one back, archiving the current one. Facts that outlive a session live
  in memory (7.7), not in history.
- **R-AGT-7.** Who the agent is and how it talks is the operator's data,
  not code. It is a set of Markdown files in the node's workspace
  (`persona/*.md`), read every turn. The core adds only its own rules in
  front: tool output is data, and the agent is honest about what it cannot
  do. Without persona files a neutral default applies. A first-run
  conversation that writes the persona comes with the agent's tools (M3b)
  ([ADR-0024](adr/0024-persona-as-workspace-files.md)).

### 7.3. Model router

Takes a task and returns an ordered list of "provider + model" candidates.
The skill declares its requirements in the `SKILL.md` frontmatter:

```yaml
model:
  tier: cheap          # cheap | smart
  tools: true
  context: short       # short | long
  vision: false
  privacy: local-only  # data never leaves for the cloud
```

| Layer | What it does | Phase |
|---|---|---|
| 1. Hard filters (tags) | Drops models without the required capabilities; `privacy: local-only` keeps local models only | P1 |
| 2. Rules and weights | Daily budget, worker health and exhausted limits, event type (cron → cheap, human → smart); ranking by price, tier and latency with weights from config | P1 |
| 3. Fallback chain | Error, 429, timeout or exhausted limit — take the next candidate | P1 |
| 4. Classifier | A cheap model estimates task complexity when the skill sets no `tier` | P2 |
| 5. Learning | Who succeeded, at what cost, how the human reacted — the rules adapt | P3 |

- **R-RTR-1.** Each model carries capability tags (tools, vision, context),
  price per token, latency, local/cloud. The price and context catalogue may
  be taken from LiteLLM's open JSON rather than maintained by hand.
- **R-RTR-2.** The P1 router is rules + weights, implemented in Go (a couple
  of hundred lines). LiteLLM / OpenRouter / RouteLLM are sources of ideas,
  not dependencies.
- **R-RTR-3.** The router also picks where a tool runs (from the node
  registry); P1 has one node, but the interface accounts for more.
- **R-RTR-4.** Skills (M3c, levels 1 and 2 of 9.8) live in
  `workspace/skills/<name>/SKILL.md`; built-in skills are embedded in the
  binary and reserved by name, so a workspace skill of the same name is
  not loaded and the owner is told. The system prompt lists each skill's
  name and description; the model reads one with `skill.read`, and the
  rest of the turn is routed by that skill's `model` block. Only the
  owner installs a skill; the agent may propose one by showing its text.
- **R-RTR-5.** A level-2 skill declares its script in the frontmatter:
  `script: {run, effects, env, timeout}`. `effects: read` runs by
  itself, `effects: change` asks with a button; the output is outside
  content. The script runs without a shell, in its own process group,
  with an environment built from scratch plus the names it lists (the
  node's secrets never pass), a timeout of at most 120 s, and capped
  output. Until the sandbox of
  [ADR-0039](adr/0039-command-sandbox.md) (M3d) it can read what the
  node's user can read
  ([ADR-0042](adr/0042-skills-and-scripts-before-the-sandbox.md)).

### 7.4. Providers and model access

- **R-PRV-1.** The first client is OpenAI-compatible Chat Completions: one
  client covers a cloud provider, Ollama, OpenRouter and the like.
- **R-PRV-2.** The native Anthropic Messages API is the second client, added
  when needed.
- **R-PRV-3.** Tools are described in MCP format; the core translates them
  into the specific API's schema.
- **R-PRV-4.** The claude CLI runs **unmodified** as `claude -p`. As a
  worker it does standalone tasks (write a module, analyse logs; P2). As a
  conversation provider it gets the core's history in the prompt on every
  call, with no session and no `--resume`
  ([ADR-0021](adr/0021-providers-chosen-by-the-operator-claude-cli-included.md)).
  The agent's tools reach it through the MCP bridge (R-PRV-6).
- **R-PRV-5.** The operator chooses the providers a node uses: one or
  several, of any supported type (`openai`, `claude-cli`; `anthropic`
  later), listed in the model catalogue. The router ranks and falls back
  across all of them.
- **R-PRV-6.** A CLI provider that runs its own tool loop (the claude CLI)
  gets the agent's tools through an MCP bridge to the core (M3b):
  - the core serves the turn's tools for the duration of one run,
    authenticated by a per-run grant;
  - the session, the principal and the turn come from the core's own
    context, never from the child process;
  - the CLI's own tools stay off;
  - every call runs in the core under the same policies, taint, journal
    and idempotency keys as for an API model; the bridge only forwards.

  The transport is a per-run Unix socket with a token, reached through
  a hidden `ganglion mcp-relay`
  ([ADR-0036](adr/0036-mcp-bridge-over-a-per-run-unix-socket.md),
  Proposed), served by an own minimal MCP server
  ([ADR-0037](adr/0037-own-minimal-mcp-server.md), Proposed).

Rules for access to Anthropic models (per
[Claude Code legal and compliance](https://code.claude.com/docs/en/legal-and-compliance),
checked 2026-10-31):

| Method | In the project |
|---|---|
| Anthropic API with the user's own key | Yes, the default |
| OpenAI-compatible and local models | Yes |
| A worker with an unmodified `claude`, the user logs in through Anthropic's own flow | Yes, as an option; the project never asks for, stores or forwards tokens |
| Taking a subscription OAuth token and calling the API with it directly | No, forbidden |
| A built-in "Sign in with Claude" | No |
| One subscription worker serving other people's nodes | No |

Routine work (conversation turns, crons, consolidation) goes to whatever
the operator configured. A cheap API model is the recommendation; a
subscription through the claude CLI is allowed for the owner's own node,
within its limits (ADR-0009, ADR-0021).

### 7.5. Runner and WASM modules

Modules always run inside a runner. A runner consists of:

- **wazero** — WASM in pure Go, no CGO;
- **embedded NATS** (`nats-server` as a library) — a leaf node with on-disk
  JetStream (P2; P1 may use an in-memory bus);
- **module supervisor** — start, limits, restart, version switching;
- **action policies** — every host function call and `tool.call` is checked;
- **scheduler** — module cron triggers work without a link to the core;
- **MCP subprocess host** — for non-WASM modules (Python and so on).

**Module ABI.** A module exports `handle(trigger, payload) → result` (`init`
and `health` are planned for P1, with the supervisor). It sees only the host functions its manifest grants:

| Host function | Provides |
|---|---|
| `log` | Structured logging |
| `state.get` / `state.put` | Module state in KV; survives version swaps |
| `http.fetch` | HTTP to allowed domains only |
| `event.emit` | Events from an allowed list |
| `tool.call` | Tool calls, through policies |
| `secret.get` | Secrets from an allowed list; the model never sees them |
| `config.get` | Values from the manifest's `config` block (e.g. a device serial number) |
| `now` | Current time |

Example manifest:

```yaml
name: deye-monitor
version: 3
triggers:
  - cron: "*/30 * * * *"
  - tool: deye.status        # the model can call it as a tool
capabilities:
  http.fetch: [eu1-developer.deyecloud.com]
  secret.get: [DEYE_APP_ID, DEYE_APP_SECRET, DEYE_EMAIL, DEYE_PASSWORD]
  event.emit: [battery.changed, module.exception]
  state: true
limits:
  memory: 8MiB
  timeout: 20s
escalate:                    # when to call the model
  - on: module.exception
  - on: auth_failed
```

- **R-RUN-1.** By default a module is granted nothing; only what the manifest
  lists.
- **R-RUN-2.** Memory and per-call time limits in wazero; on overrun or trap
  the call is aborted and the instance recreated.
- **R-RUN-3.** Unexpected data or a module error emits a `module.exception`
  event to the agent.
- **R-RUN-4.** One artifact for ARM, x86 and RISC-V. On amd64/arm64 wazero
  compiles to native code; on 32-bit ARM and RISC-V it interprets.
- **R-RUN-5.** Modules use ganglion's own ABI on wazero, not Extism
  ([ADR-0011](adr/0011-module-abi.md)): every host call, HTTP included, goes
  through our code, so policies, audit and timeouts apply to each request,
  and a failed request is an error the module handles, not a trap. Module
  glue exists for Go/TinyGo (`pdk/guest`) and Rust (`modules/deye-rs`); a new
  language needs its own small glue.
- **R-RUN-6.** The runner process sets a soft memory limit (`GOMEMLIMIT`) from
  its config, and edge JetStream streams have explicit size limits, so a long
  outage cannot grow memory or the backlog without bound (task 0.5).
- **R-RUN-7.** A module can be a tool of the core: the agent calls it
  through the same policies as any tool. The first one is `html2md`
  (7.11), a pure function granted no host functions, embedded in the
  binary (M3b). M4 extends the path to modules from the node config
  (`deye.status`).
- **R-RUN-8.** Modules are installed, updated and rolled back without
  restarting the node (M3c, admin only):
  - the source in P1 is a URL plus a sha256 sum; built-in modules come
    from the project's GitHub Releases, tagged per module
    (`html2md/v0.3.0`);
  - the new version is checked against its sum and passes a self-test on
    built-in samples before an atomic switch; calls in flight finish on
    the old version; a failed self-test keeps the old version and tells
    the owner;
  - the previous version is kept for `/modules rollback <name>`; module
    state survives the swap;
  - an updated version stored on the data volume takes precedence over
    the embedded one.

  Trust boundary:
  - built-in modules (`ganglion/…`) come only from the project's release
    channel (a checksum now, a signature in P3); nobody else, the agent
    included, replaces one under the same name;
  - modules built later by the agent install under `local/…`, always
    with the owner's approval;
  - a new version's manifest rights are compared with the old one's, and
    any widening is a separately worded approval;
  - the core embeds, for each built-in module, the ABI version and a
    ceiling of rights; a release that asks for more is refused until the
    core itself is upgraded. html2md's ceiling grants no capability, so
    in M3c a widening is refused outright; the worded approval comes
    with modules that have rights (M4).

  The owner drives it with `/modules`, `/modules update <name>` and
  `/modules rollback <name>`, without a model
  ([ADR-0041](adr/0041-module-install-update-and-trust-boundary.md)).

Resources, measured in P0 on amd64 under a 128 MB limit (task 0.5,
[research note](research/2026-11-01-runner-memory.md)); arm64 confirmation
on a Pi is pending:

| What | Memory | Note |
|---|---|---|
| Runner with one module | 17–38 MiB steady, 23–78 MiB peak at load | Rust lowest, standard Go highest; the peak is wazero compiling the module |
| Leaf node with JetStream | 18 MiB idle; 45 MiB with 117 MB buffered and `GOMEMLIMIT=40MiB` (80 MiB without) | Set a soft limit and stream size limits |
| Edge binary | ~9 MB runner + wazero today; ~25–30 MB with embedded NATS (estimate) | Cross-compiled for arm64 |
| One module | 161 KB (Rust) to 3.6 MB (Go) of `.wasm` | Memory per manifest limit |
| Target hosts | A small container of about 128 MB, or a Pi Zero 2 W (512 MB, arm64) | Principle 8 |
| Microcontrollers | — | A runner does not fit; they connect over MQTT |

### 7.6. Action policies and risk classes

A human sets the risk class once. The agent may propose a class but may not
lower it.

| Class | Examples | Requirements |
|---|---|---|
| Informational | Notifications, reports | Automatic |
| Comfort | Lights, boiler shedding | Automatic + debounce; manual override wins |
| Physical safety | Gates, locks, alarms | Two factors or confirmation, time windows, trigger limits, `ttl`; when unsure — closed; manual control always works |

Confirmation is layered:

- **System** — deterministic checks: kill switches, invariants, a second
  factor (BLE, a sensor), time windows, limits.
- **Reviewer model** (preferably another provider) checks the action against
  the policy. It can block, but cannot by itself allow something dangerous.
- **Human** — yes/no in Telegram with a `ttl`; for critical actions, the
  admin only or two people.

Combinations: comfort — system only; gates — system + (second factor or
human); module install — tests + reviewer + admin.

- **R-POL-1.** Policies are enforced on the node that executes the action.
- **R-POL-2.** No human answer within `ttl` in the physical-safety class
  means denial.
- **R-POL-3.** Every side-effecting action is written to the audit log.
- **R-POL-4.** Side-effecting tools require human confirmation by default;
  external text (messages, web pages, emails) is marked as data (prompt
  injection defence).
- **R-POL-5.** Kill switches are switch cells per action/device plus a
  global one ([ADR-0014](adr/0014-state-cells.md)); toggled by a Telegram
  command.
- **R-POL-6.** A session is **tainted** once external content enters it: a
  web page, a search result, a forwarded message (a quote of another
  message and a reply to a message in another chat count too: the
  transport marks them and the agent wraps that text as data), a
  scheduled task's fetch.
  - The taint is per session, not per turn: the content stays in the
    history and would steer the next turn.
  - It lasts until the session ends (`/new`, idle reset, R-AGT-6) and
    survives compaction. The compaction summary keeps external content
    marked as data, so an injection cannot come back as the agent's own
    words.
  - In a tainted session these need the owner's confirmation: any side
    effect, any send to anyone but the requester, and any memory write.
- **R-POL-7.** A **clean-context check** guards what the agent keeps. It
  uses a cheap model with no history, no tools and no tainted context (the
  "dual LLM" pattern; a first, narrow form of the reviewer model above).
  - **On write:** every memory entry is checked before it is committed: a
    fact about the owner, or a hidden instruction? Imperative entries
    ("always do…", "ignore…") are rejected. Suspicious ones go to
    quarantine, and the owner gets a notice.
  - **On schedule:** a system job audits memory, persona and skills for
    injections with the same check, in code and without a model turn,
    and reports to the owner. It is marked as a system job and cannot be
    deleted. Contradictions and stale entries are left to nightly
    consolidation (R-MEM-1), which has a model.
- **R-POL-8.** A scheduled agent task carries a **grant**, fixed when the
  task is created.
  - The creation confirmation shows what the runs may do: the
    instruction, the schedule, the tools, the hosts `web.fetch` may reach,
    the exact host commands the runs may execute with their consequence
    lines (R-CMD-6, M3d) and who receives the reply. The owner approves it once; the runs then
    need no presses.
  - A run's own fetches taint its session (R-POL-6), but inside its grant
    the run still needs no confirmation: the grant approved at creation
    is the confirmation.
  - A run that needs anything outside its grant is denied, and the owner
    gets one notice with the reason, not a button on every run.
  - A task runs as its creator, with the creator's rights. Its
    instruction and grant cannot be changed by what a run reads.

The rest of the baseline is in
[ADR-0031](adr/0031-agent-security-baseline.md) (Proposed): no data out
without confirmation (R-WEB-2), secrets redacted before any journal or
log, least privilege, every decision journalled (R-OBS-5), and tools,
memory and scheduled tasks for admins only in M3b; the family bot
migration (M7) decides what `family` members get.

### 7.7. Memory

The model does not learn — the system does: it accumulates files that the
model reads every turn. Everything is in git, so "learned something bad" is
undone with `git revert`.

| Level | What it holds | Where | How it reaches a turn |
|---|---|---|---|
| Working | The current conversation | History in the core's SQLite | Recent turns + summary |
| Episodic | What happened and when | `turns` + device events | On demand via `memory.search` |
| Semantic | Facts about people, home, preferences | `memory/*.md` + embeddings | Index in the system prompt + semantic search |
| Procedural | How to do specific things | Skills: instruction + script + model requirements | Description in context, body on demand |

- **R-MEM-1.** Nightly consolidation is a regular cron with its own prompt:
  re-read the day's turns, extract facts into `memory/*.md`, fix
  contradictions, delete stale entries, propose a skill for a recurring
  procedure, commit and re-index embeddings. Archived sessions it has
  read may then be dropped after a retention (section 11, M7).
- **R-MEM-2.** A dislike or a correction in chat flags the turn;
  consolidation handles flagged turns first.
- **R-MEM-3.** Memory is Markdown files in the node's workspace, in git.
  ganglion's formats are its own. Moving in from another assistant goes
  through an importer that writes these files (spec 14).
- **R-MEM-4.** P1 uses files; embedding search and edge slices are P2.
- **R-MEM-5.** Memory holds facts, not instructions (M3b).
  - The agent decides what to remember; "remember this" is one case of it.
  - Facts reach the model as a "known facts" data block. Procedures go to
    skills, which the owner installs; directives and policies go to
    config and code.
  - What was said in an earlier chat (episodic) is not a stored fact:
    recall labels each hit as one or the other, with its date, and the
    agent says which it reports.
  - Every entry records where it came from: the turn, whether the
    session was tainted, the date. Writes from a tainted session need
    confirmation (R-POL-6), every write passes the clean-context check
    (R-POL-7), and every change is a git commit.
  - "What do you remember about me?" lists it.

### 7.8. Security and operations (MVP)

| Threat | Measure | Phase |
|---|---|---|
| Prompt injection | Confirmation of side effects; external text is data; tainted sessions, the clean-context check and grants (R-POL-6–8); the `web.fetch` URL rule (R-WEB-2); [ADR-0031](adr/0031-agent-security-baseline.md) | P1 |
| Secret leakage | The model never sees secrets; modules get them via `secret.get` / env per manifest; secrets are redacted before the journal and logs, and the Bot API token is never logged | P1 |
| A stranger messages the bot | `allowed` table with roles: the admin installs modules, others only ask; in M3b a `family` member gets conversation with no tools and no memory, and the family bot migration (M7) decides family access | P1 |
| Diagnostics expose the node | `ganglion mcp` opens no port: it runs over stdio and relies on the platform's authentication; read access to the data directory is read access to all history | P1 |
| A host command reads secrets or gains root | Commands run in a sandbox: no privilege gain, a clean environment, the node's process and secret files hidden; root and anything that starts a process outside the sandbox are forbidden; the tool is off where the sandbox cannot be set up (R-CMD-1–3, [ADR-0039](adr/0039-command-sandbox.md)) | P1 (M3d) |
| The node's own files fill the disk | Everything the node creates is accounted for (what, size, age) and cleaned by age or size (R-CMD-8) | P1 (M3d) |
| Infinite loop | R-AGT-4 | P1 |
| Exhausted limits | Crons and consolidation on a cheap model; usage counter in `turns` | P1 |
| Self-modification broke something | Regression scenarios (question → expected tool call) after every change | P3 |
| Lost volume or corrupted DB | Daily backup of SQLite (`VACUUM INTO`) and the memory git repo to S3 or a private remote | P1 |

### 7.9. Observability

[ADR-0026](adr/0026-opentelemetry-prometheus-and-promtop.md).

- **R-OBS-1.** The code is instrumented through the OpenTelemetry API:
  - spans for a turn, a model call, a tool call, an outbox send and a
    Telegram API call;
  - metrics for turns, model calls, tokens, cost, sends, retries and
    dropped messages;
  - the Go runtime and the process under their standard Prometheus names
    (`go_*`: goroutines, heap, GC; `process_*`: CPU, resident memory,
    open files, start time).
- **R-OBS-2.** Metrics are served in the Prometheus text format at
  `/metrics` on a local address (`GANGLION_METRICS_ADDR`, default
  `127.0.0.1:9464`). The promtop web dashboard is at `/webmetrics/`, for
  localhost only.
- **R-OBS-3.** Traces go out over OTLP/HTTP when the standard
  `OTEL_EXPORTER_OTLP_ENDPOINT` (or `…_TRACES_ENDPOINT`) is set; otherwise
  they are not recorded.
- **R-OBS-4.** Logs are slog. A line written inside a span carries
  `trace_id` and `span_id`. Message text and model output are never
  logged, traced or exported as metric labels.
- **R-OBS-5.** Every tool call is written to a `tool_calls` journal beside
  `turns` (M3b): the turn id and call number, the tool, the arguments
  (secrets redacted, size-capped), the result's size and head, the error,
  the duration, the policy decision and the taint. One turn id ties the
  incoming message, the route, the calls and the reply together, so a
  model can diagnose a turn without joining events by time. It is also
  the audit log of R-POL-3. Rows are kept for a limited age.
- **R-OBS-6.** Logs are also written to disk as JSON lines in
  `<data>/logs/`, beside stdout (M3c). Writes are synchronous, so the last
  lines before a `kill -9` survive. Files rotate by size with limits on
  their count and age, so the disk never fills. Secrets are redacted.
  `<data>` is the directory of the database; `--log-dir`
  (`GANGLION_LOG_DIR`) moves the logs, and `off` keeps them on stdout
  only ([ADR-0043](adr/0043-logs-on-disk-with-an-own-rotator.md)).
- **R-OBS-7.** `ganglion mcp` is an MCP server over stdio for diagnosis
  from outside (M3c). It needs no running node and no model: it opens the
  database read-only and the log files, and offers read-only tools (search
  logs and the journal, recent failed turns, the effective configuration
  with secrets redacted, queue state). It is reached by running the
  command, locally or through `ssh`, `docker exec` or `kubectl exec`; it
  opens no port. The agent gets the same search tools, for admins only,
  with results marked as external data. `config.show` shows the
  configuration this process reads (the flags and the environment), each
  secret only as set or unset; `turn.show` gives one turn whole: the
  event, the route, every call with its decision, and the replies
  (R-OBS-5) ([ADR-0044](adr/0044-ganglion-mcp-diagnostics-over-stdio.md)).
- **R-OBS-8.** Everything significant about how the node works is a
  metric, not only a log line or a journal row (owner, 2026-11-03). That
  covers:
  - speed and errors;
  - model calls, context size and cost;
  - tool calls, policy decisions and confirmations;
  - module runs, the scheduler, the queues and delivery.

  The dashboard shows only what `/metrics` has, so a missing metric is a
  blind spot. A change that adds such behaviour adds its metric in the
  same commit, beside the log line and journal row that R-OBS-4–7 ask for
  diagnosis. The metric shows how the node is doing; the log line shows
  why one case went wrong. The owner sees the metrics in Telegram (M3c):
  `/metrics`, without a model, and the agent's `metrics.show` send a
  promtop picture rendered in process from the node's own registry, never
  through `web.fetch`. Both take a choice (owner, 2026-11-03, after the
  first live picture): `all` adds what the dashboard hides by default, a
  group (`ganglion`, `runtime`, `process`) keeps that section, and any
  other word is a filter over the metric names (a substring, or globs
  with `*`).
- **R-OBS-9.** In a network (P2) the core collects the telemetry of every
  node over the bus (owner, 2026-11-03). An edge dials out and opens no
  port, so nothing can scrape its `/metrics`.
  - **Metrics.** Every N seconds a node publishes a snapshot of its
    metrics to `a.<network>.<node>.telemetry` over plain NATS, without
    JetStream. Nothing is stored on the way, and the counters are
    cumulative, so a lost snapshot costs nothing. The core adds a `node`
    label and serves the whole network on its own `/metrics`, so one
    dashboard shows every node.
  - **Traces.** The trace context rides in the envelope's headers, so one
    turn that calls a tool on an edge is one trace.
  - **The error journal.** The edge's error log and its `tool_calls` rows
    go through JetStream, with limits on size and age. They survive a
    lost link, and `ganglion mcp` on the core can read them (R-OBS-7).
  - **Trust.** The core takes `node` from the subject, never from the
    payload; NATS permissions let a node publish only under its own
    subject (9.3). The core caps series and snapshot size per node. On an
    edge the R-OBS rules hold as on the core: no content, labels from
    closed sets.
  - **Outside collectors.** Forwarding to an outside collector (OTLP, a
    Prometheus remote write) is a later feature of the core. It is not a
    second path from each edge: the bus stays the only link an edge needs. Only what is safe to collect: no content, no identifiers
  of people or conversations, and label values from closed sets.

### 7.10. Scheduler and reminders

The scheduler moves up from M4 to M3b; M4 reuses it for module triggers.

- **R-SCH-1.** Crons run in the node's time zone (`GANGLION_TZ`, default
  Europe/Kyiv) and are recorded in `cron_runs`. After downtime each missed
  job runs once, not once per missed slot. A run's `idemKey` is the job
  plus the scheduled time.
- **R-SCH-2.** The agent schedules two kinds of jobs:
  - a **reminder**: a fixed text at a time;
  - a **task**: an instruction run as a full agent turn with tools.

  Both are created and listed in words; a list comes with buttons
  (Details, Delete, and Run now for tasks). Creating a recurring task asks
  for confirmation, because it runs unattended and spends tokens, and
  fixes its grant (R-POL-8). Each task has its own session, so its
  earlier runs are its history. Scheduled turns count towards the daily
  limit (R-AGT-4).

### 7.11. Reading the web

- **R-WEB-1.** `web.fetch` runs in the core:
  - http(s) only; no private or loopback addresses, re-checked after
    every redirect; size and time limits;
  - HTML goes to the `html2md` WASM module, which extracts the main
    content and returns Markdown; it is granted no host functions, so
    hostile HTML is parsed in the sandbox, not in the core (R-RUN-7);
  - JSON and plain text pass through unconverted;
  - the result is marked as external data and truncated to a limit.
- **R-WEB-2.** `web.fetch` runs without confirmation only on a URL that
  came from the owner's message or from search results. A URL the model
  built itself needs confirmation: its query string is a channel out
  ([ADR-0031](adr/0031-agent-security-baseline.md)).
- **R-WEB-3.** `web.search` has one backend behind an interface. The
  default needs no key; keyed backends are enabled by setting a key. The
  backends are chosen in [ADR-0035](adr/0035-web-reading.md): Brave with
  a key, DuckDuckGo without one.

### 7.12. Host commands and plans

M3d, after M3c. Design: [M3d design](plans/p1-m3d-host-commands-design.md);
decisions: [ADR-0038](adr/0038-host-commands-as-argv-with-classes-in-code.md),
[ADR-0039](adr/0039-command-sandbox.md),
[ADR-0040](adr/0040-plans-as-approvals.md) (Proposed).

- **R-CMD-1.** `host.exec` runs a program with a list of arguments, with
  no shell, on the node that serves the conversation, as the node's Unix
  user; for admins only. A kill switch cell turns it off. Its output is
  capped, redacted, wrapped as external data and taints the session
  (R-POL-6). Every call is journalled (R-OBS-5).
- **R-CMD-2.** A table in code puts every command in a class by its
  program, subcommand and flags; the model never chooses the class:
  - **read** (sizes, names, state: `df`, `du`, `free`, `ls`, `ps`
    without environment, `docker ps`, `docker system df`) runs by itself;
  - **content** (`cat`, `grep`, logs, `docker inspect`, `ps e`), **change**
    (`rm`, `go clean`, `docker image prune`, `docker rm`,
    `systemctl --user stop`, `kill`, any program not in the table) and
    **script** (`bash`, `python3`, `find -exec`) need the owner's press;
  - **forbidden** never runs: `sudo`, `su`, `doas`, `pkexec`, `env` and
    `printenv` without a program, and anything that starts a process
    outside the sandbox or leaves one to run later (`docker run`,
    `docker exec`, `systemd-run`, `systemctl --user start` and `enable`,
    `crontab`, `at`).

  Moving an entry towards read widens rights and is the owner's decision,
  in configuration the agent cannot edit.
- **R-CMD-3.** Every command runs in a sandbox: it cannot gain privileges
  (`no_new_privs`), gets a clean environment, cannot see the node's
  process, and cannot read the node's data directory, its secret files,
  `.env` and `.env.*` files in any folder, or a default list of the
  owner's secret stores. If the node cannot set
  this up, or runs as root, `host.exec` is not offered and the node says
  why.
- **R-CMD-4.** A command longer than a short wait continues in the
  background; its completion resumes the conversation with the result.
  Commands have a hard time limit, their own process group and a cap on
  how many run at once.
- **R-CMD-5.** `host.stats` reports, without a subprocess: memory (total,
  available, the node's RSS), load, uptime, free and used disk for `/`
  and the data directory, and the sizes of the node's own folders. It
  works where `host.exec` is off.
- **R-CMD-6.** Every table entry carries a fixed consequence line in
  plain words, in English, Russian and Ukrainian, shown wherever a press
  is asked for; the model's own reason is shown apart and marked as the
  agent's words. Reading a file that git ignores in its repository adds
  a warning that such files often hold passwords and keys; ignored paths
  are not hidden, since caches and logs live there too.
- **R-CMD-7.** Before work with several steps the agent posts a plan
  message and edits it as the steps run; it stays in the chat with the
  outcome. A plan with no step that needs a press runs without one. A
  plan with such steps has one "Run" button that approves exactly the
  listed commands, argument for argument, in this conversation, until the
  plan ends or expires. A command that needs a press and is not in the
  approved plan does not run: it gets its own confirmation or a new plan.
- **R-CMD-8.** The node accounts for everything it creates on disk (what,
  size, age) and cleans it by age or size; `host.stats` reports it. The
  claude CLI work directory is the first case.

## 8. First flow: Deye and load shedding (P1)

Deye battery monitoring plus staged shedding of loads by battery state of
charge (SOC). A scaled-down reference scenario: sensor, rules, action,
notification — all without an LLM.

The flow acts on **state cells**, not on devices
([ADR-0014](adr/0014-state-cells.md)): each load is a switch cell with a
*desired* state the flow sets and a *reported* state from its backend. In
P1 the loads use the `virtual` backend and a Telegram notifier observes
their changes; Tuya plugs become the backend when they are linked, without
changing the flow.

### 8.1. States

```
              SOC < 50% and grid down          SOC < 40%
 ┌──────────────┐ ───────────▶ ┌──────────────────┐ ──────▶ ┌──────────────────┐
 │ All on       │              │ Big boiler off,  │         │ Both boilers off │
 │              │ ◀─────────── │ kitchen one on   │ ◀────── │ (base load only) │
 └──────────────┘ SOC ≥ 60%    └──────────────────┘ SOC≥50% └──────────────────┘
                  or grid back                      or grid back
 In every state: data older than 20 min → touch nothing;
 respect a manual switch-on for 2 h; a kill switch per load and a global one.
```

### 8.2. Rules

| Rule | Value |
|---|---|
| Thresholds | 50% — big boiler, 40% — kitchen boiler. In the flow config, not in code |
| Condition | Only while the grid is down. If the inverter drains the battery even with grid, drop this condition |
| Restore | +10% hysteresis: big at ≥ 60%, kitchen at ≥ 50%; or when the grid returns — one at a time, 1 min apart |
| Debounce | Each load cell changes desired state at most once per 15 min |
| Polling | 30 min with grid, 5 min without (at 5 kW, 10% of a 10 kWh battery is gone in ≈ 12 min); at most 288 calls a day, about 9% of DeyeCloud's free 100,000 calls a month in the worst case. On HTTP 429 back off, honouring `Retry-After` (task 0.7) |
| Data freshness | Older than 20 min — switch nothing, report it |
| Manual override | A load's reported state changed without our command (someone switched it by hand) — automation stays off it for 2 hours and reports it. Applies only to backends that report state |
| Kill switches | Switch cells per load and a global one; off means notifications only. Telegram command sets their desired state |
| Notifications | A Telegram observer posts silently to the notifications topic on every change of a load cell |
| Escalation to the model | Deye or backend auth error, a backend not converging desired and reported, no data for over an hour |

### 8.3. Blocks

Reusable blocks:

- `deye-reader` — reads SOC and grid status into sensor cells (done in P0 as
  a WASM module);
- load cells `load.big_boiler`, `load.kitchen_boiler` — switch cells; backend
  `virtual` in P1, `tuya` once the plugs are linked;
- a Telegram notifier — an observer of the load cells;
- `load-shedding` — the flow itself, the state machine from 8.1, which only
  reads sensor cells and writes the desired state of load cells.

The bot still answers "how is the battery?" through the `deye.status` tool.

**Tuya access** (when the plugs are linked; P1):

- **Cloud (Tuya OpenAPI)** — a project on the Tuya developer platform plus a
  linked Smart Life account; works from cloud hosting, like DeyeCloud.
- **Local (LAN protocol, device key)** — only from a runner at home, but no
  dependency on the cloud or the internet.

The flow matters precisely during a power cut. The home router runs on the
inverter and its own battery, and on 2026-11-01 DeyeCloud kept serving fresh
data during an outage, so the cloud path works when it is needed (task 0.7).
A home runner with LAN access (P2) adds independence from the cloud but is
not required for the first flow.

### 8.4. Checks before enabling

- **R-LS-1.** Replay of the SOC history (samples every 30 min already
  accumulate in `deye.sqlite3`) and synthetic discharge curves during an
  outage.
- **R-LS-2.** Invariants covered by tests on `virtual` cells, including
  injected failures and manual changes: a load never changes more often than
  once per 15 min; never switches on while SOC is below the threshold and the
  grid is down; nothing happens on stale data.
- **R-LS-3.** Shadow mode is the `virtual` backend: the flow runs for real,
  the notifier reports every change, no device is touched. Moving a load to
  the `tuya` backend raises its risk class and needs the owner's approval.
- **R-LS-4.** Until the bot migrates, the WASM module runs alongside
  `deye.py` and their outputs are compared.

## 9. P2–P3: outline

Direction and invariants. Formats are drafts and get fixed at the start of
the corresponding phase.

### 9.1. Core ↔ edge protocol (P2)

The protocol is a control plane around MCP, not a replacement. Tool
descriptions and calls are ordinary MCP messages (JSON-RPC) inside an
envelope. We add only what MCP lacks: node registration, events, module
delivery, policies and context exchange.

Envelope:

```json
{
  "v": 1,
  "id": "01JC3W5Q8Z",
  "type": "tool.call",
  "from": "core",
  "to": "edge-garage",
  "corr": "turn-4812",
  "ttl": 30,
  "body": { "jsonrpc": "2.0", "method": "tools/call", "params": { "name": "gate.open" } }
}
```

| Type | Direction | Purpose |
|---|---|---|
| `node.hello` / `node.welcome` | edge → core / core → edge | Version, capabilities, tool-list hash; config and policies in reply |
| `node.heartbeat` | edge → core | Lease, health, load; no heartbeat — the node's tools are unavailable to the router |
| `tool.register` | edge → core | MCP schemas (`tools/list`) + required permissions |
| `tool.call` / `tool.result` | core ↔ edge | MCP `tools/call` inside the envelope |
| `event` | edge → core | Device or module event with `idemKey` |
| `context.get` / `context.put` | edge ↔ core | A memory slice for offline work; new facts back |
| `module.deploy` / `activate` / `rollback` | core → edge | Reference to a signed WASM, blue/green |
| `policy.update` | core → edge | New action rules |
| `cmd` | core → edge | Restart, config, drain |
| `telemetry` | edge → core | A periodic snapshot of the node's metrics (R-OBS-9) over plain NATS, not JetStream; spans and the error journal of R-OBS-9 travel beside it |

Rules: NATS as the transport (subjects `a.<network>.<node>.<type>`, queue
groups for identical tools on several edges), MQTT and WebSocket adapters;
JSON to start, CBOR for microcontrollers, one schema for both; an unknown
`type` gets an `unsupported` reply, each type is versioned separately,
extensions use reverse-DNS identifiers, as in MCP (ADR-0013); at-least-once + `idemKey`; every command
has a `ttl` ("open the gate" delivered two hours late must expire); an edge
without a link keeps running on its modules and local policies, events queue
up in JetStream.

Checked in P0 task 0.4 against MCP 2026-07-28 and A2A 1.0
([research note](research/2026-11-01-mcp-a2a-coverage.md)): MCP covers tool
discovery and calls and is carried as-is; the rest of this table is ours.
The decision, including how connectivity and trust come from NATS, is
[ADR-0013](adr/0013-protocol-envelope-around-stateless-mcp.md).

### 9.2. Module updates: desired state (P2)

Argo CD style: the core writes what should run on a node; the runner
converges to it. NATS KV (history, watch) + Object Store.

1. `.wasm` goes to Object Store (bucket `modules`); KV `modules.<name>` holds
   version, sha256, signature and manifest.
2. KV `nodes.<node>.desired` holds the required version; a canary node first.
3. The runner watches its key, downloads, verifies sha256 and the owner's
   signature.
4. `init` + `health`; on failure the new version is dropped and the old one
   keeps running.
5. Blue/green: the new version takes over triggers, the old one finishes its
   calls and unloads; module state in KV survives the swap.
6. Actual state goes to `nodes.<node>.actual`. Rollback is writing the
   previous version to `desired`.

Watchdog at four levels: call (wazero deadline and memory limit) → module
(supervisor, exponential backoff; N crashes in M minutes — disable and emit
`module.crashloop`) → runner (systemd `WatchdogSec` + `sd_notify` or a
container restart) → node (heartbeat key in KV with TTL; expired — node
offline; offline for long — alert a human).

### 9.3. Network security (P2)

NATS covers transport: TLS/mTLS, decentralised auth (operator → account →
user, JWT + NKey), publish/subscribe permissions (an edge sees only
`a.<network>.<own node>.>`), isolation via accounts, JWT revocation. No
separate VPN is needed.

We add: credential issuance when adding a node (QR code or one-time token);
per-role permission templates; module signing with the owner's ed25519 key —
a runner runs only signed modules; module secrets encrypted to the node's
key (x25519 from nkeys); a "disable node" button; audit in JetStream.

An edge needs three files: the binary, a NATS credentials file, the owner's
public key.

### 9.4. Skill compilation (P3)

A user asks for something regular → the model writes a module for the task →
it then runs on its own.

1. The model describes the contract: trigger, inputs, tools, output, when to
   call the model.
2. It writes the code (TinyGo, Rust or AssemblyScript) and tests.
3. The build runs on a worker with a toolchain, not on the edge.
4. Sandbox tests → summary in Telegram (what it does, which permissions,
   test results, diff) → "Install" / "Reject" buttons.
5. The module is signed and rolled out via desired state.
6. An exception or `module.crashloop` → the model investigates and ships a
   new version the same way. Permission expansions are shown separately in
   the summary.

Build caches: each module has its own build cache (in Docker, a volume
labelled with the module). Every build renews its TTL; after a while with
no builds (7 days by default, set by the operator) the cache is dropped,
while the module, its sources and its signed build stay. Shared caches
(crates, Go modules, toolchain images) are trimmed by size, least recently
used first. Dropping a cache needs no press and is reported in one line
(owner, 2026-11-03, R-CMD-8).

Threats and measures: module overreaches → only manifest host functions;
tampering → signatures; rogue node → NKeys/mTLS and a trust level per node;
hang → wazero limits; module written under prompt injection → install only
with confirmation, diff and permissions in the summary.

### 9.5. Distributed tool pool (P2–P3)

Each node declares its tools in a registry on NATS: name, MCP schema, where it
runs, required permissions. The router picks both the model and the execution
location: a camera task on the node with the camera, heavy processing on the
server.

### 9.6. Flow engine (P3)

A state machine of reusable blocks on the runner; the model writes the flow
description and only the missing blocks. Kill switches in KV with an API, a
simulator with event replay, shadow mode, risk classes in policies. A
possible second backend: compiling flows into Home Assistant automations.

### 9.7. North star: the gate

Not a plan phase but a direction. Mechanisms are proven in order: Deye (P1)
→ parking lights (low risk) → gate.

Request: "make the gate open when I drive up".
**Design, with the model:** capability registry → flow, guardrails and what is
missing → description, blocks, tests, simulator → a human approves the flow
and its risk class → shadow mode.
**Execution, without an LLM:** proximity sensor → camera snapshot → plate
recognition by a small local model → kill switch → "physical safety" policy:
plate + phone nearby → open; plate only → "is that you?" with a 60 s `ttl`;
no match → closed → event `owner.arrived` → "parking lights" flow. The model
is called only on exceptions.

Testing: a week of replayed events, synthetic scenarios, invariants ("the gate
never opens without two factors"), a week of shadow mode. Improvement loop:
metrics + feedback → each incident becomes a regression test → change →
replay on history → approval → shadow → rollout.

### 9.8. Self-modification levels

| Level | What the agent creates | Core rebuild | Isolation | Decision |
|---|---|---|---|---|
| 0. Configuration | Changes to bindings, thresholds, observers, cell backends — proposed, approved by the owner, validated, committed to git | no | validation + approval; rollback | Yes, P1 (ADR-0015) |
| 1. Skills | Markdown instructions | no | not needed | Yes, P1 |
| 2. Scripts | Python/sh next to a skill | no | environment allowlist and timeout (M3c, R-RTR-5); the filesystem and network sandbox of ADR-0039 (M3d) | Yes, P1 |
| 3. MCP modules | A separate process + manifest with permissions | no | process, limits, granted secrets | Yes, the main path, P3 |
| 4. WASM | `.wasm` in wazero | no | capability list | Yes, P1 by hand, P3 by the model |
| 5. Core rebuild | `go build`, binary swap | yes | none | **Never** |

Which part of the system each level can extend, what the core always keeps,
and when a contribution can be a WASM module are mapped in
[ADR-0017](adr/0017-extension-points.md).

Why not level 5: +0.5 GB of toolchain in the image; one bad commit and the
bot does not start, and the repairer is the bot itself; Go's `plugin` package
is brittle (same toolchain, Linux only, no unloading). Zero-downtime
replacement comes from blue/green modules instead.

## 10. Plan: phases and gates

Four phases, 8–13 weekends (an estimate, refined after P0). Each phase ends
with a gate; the next phase does not start until the gate is passed.

| Phase | What we do | Exit gate |
|---|---|---|
| **P0 · Recon** | Repository skeleton; Deye monitor hand-written in WASM with `http.fetch`; evaluate Extism, MCP transports and A2A; compare module languages (TinyGo, Go, Rust); measure runner RAM | The module runs in wazero; resources measured; ABI decided (done: own ABI, ADR-0011) |
| **P1 · All-in-one** | Core + runner in one process: state cells, inbox/outbox and Telegram, then the agent (history, rule-based router, one API provider, memory, skills, policies); Deye module and load-shedding flow as its reflexes, in production after a week of shadow mode; the agent changes the system's configuration with the owner's approval; migrate the family bot ([plan](plans/p1-all-in-one.md)) | Deye runs 2 weeks without the model; the bot replaces the current one and costs less |
| **P2 · Network** | claude CLI worker; runner on a separate Pi; protocol v0 over NATS (leaf + JetStream, JWT/NKeys); desired state; watchdog; MQTT devices; policies on the runner; embedding memory; router classifier | Link loss without data loss; module update on an edge and rollback with one command |
| **P3 · Skill compilation** | The model writes contract, code and tests; build on a worker; signed one-click install; repair on `module.crashloop`; agent-written MCP modules; Home Assistant | The model compiled and updated a module on its own; public release 0.1 |

### 10.1. P0 · Recon — tasks and acceptance

| # | Task | Done when |
|---|---|---|
| 0.1 | Repository skeleton: `cmd/ganglion` with `--roles`, packages for the interfaces in section 6, CI (build + test + lint) | `go build ./...` and tests are green in CI |
| 0.2 | Rewrite `deye.py` in TinyGo → WASM; run it in wazero with `http.fetch`, `state`, `secret.get` | The module returns the same SOC as `deye.py` against the real API |
| 0.3 | The same through Extism; build the Deye reader with TinyGo, Go and Rust under both ABIs | [ADR-0011](adr/0011-module-abi.md) accepted: own ABI or Extism, backed by a 2 × 3 table of module size, call time and runner RAM |
| 0.4 | Read the MCP transport (Streamable HTTP) and A2A specs | Recorded what part of the 9.1 protocol they cover |
| 0.5 | Measure runner RAM (wazero + embedded NATS) on a Pi or in a container limited to 128 MB | Done: fits a 128 MB container ([research note](research/2026-11-01-runner-memory.md)); a Pi run later confirms arm64 timings, not a gate |
| 0.6 | Access to Tuya plugs: cloud or local key | Moved to P1: loads are state cells with a `virtual` backend until the plugs are linked ([ADR-0014](adr/0014-state-cells.md)) |
| 0.7 | Does the home internet stay up on the inverter during an outage; DeyeCloud limits for polling every 5 min | Done: [research note](research/2026-11-01-outage-connectivity-and-deye-limits.md); internet outlasts the inverter; DeyeCloud quota 100,000 calls a month, worst case uses ~10% |

### 10.2. P1 · All-in-one — scope and acceptance

Scope: sections 7 and 8 in full. Milestones, tasks, decisions and the
cut list are in the [P1 phase plan](plans/p1-all-in-one.md). The agent
milestone is split into M3a (talks), M3b (works with the world: tools,
policies, memory, the scheduler, web reading, the MCP bridge) and M3c
(module updates, skills, attachments, logs on disk, `ganglion mcp`), as
the [M3b and M3c design](plans/p1-m3b-m3c-design.md) sets out; the
scheduler moves from M4 to M3b. M3d (host commands and plans, 7.12)
follows M3c ([M3d design](plans/p1-m3d-host-commands-design.md),
[ADR-0038](adr/0038-host-commands-as-argv-with-classes-in-code.md)).

Acceptance:

- A test bot with its own Telegram token completes a turn: message → inbox →
  model → tools → outbox → reply; killing the process at any point loses
  nothing and duplicates at most one message (R-DLV-*).
- The router returns candidates from a skill's frontmatter and falls back on
  429 and timeouts.
- The Deye module runs on cron without the model; the load-shedding flow has
  passed a week of shadow mode and is enabled; R-LS-2 invariants are covered
  by tests.
- The agent applies a configuration change the owner approved (for example
  where boiler notices go), refuses one that breaks a flow invariant, and
  rolls back on request.
- **Gate:** Deye runs two weeks without the model; the family bot has
  migrated and costs less than the current setup.

Testing uses a separate bot: two processes with the same token cannot
coexist. The family bot migrates at the end of P1; the current bot stays as a
fallback until the gate is passed.

## 11. Open decisions

| Question | Options | Leaning towards | Decided by |
|---|---|---|---|
| Language of model-written modules | TinyGo · Go · Rust · AssemblyScript | Not chosen; P0 numbers (ADR-0011) favour Rust or TinyGo over standard Go | Start of P3: how reliably the model writes and fixes each, plus the P0 numbers |
| Tuya access | Cloud · LAN | Cloud: the internet stays up during outages (0.7); LAN later with a home runner | P1, when the plugs are linked |
| Worker protocol | NATS request/reply · gRPC · HTTP | NATS: one bus for events and tasks | Two-node spike in P2 |
| History and tool format | Own · Anthropic Messages · OpenAI | Decided: neutral own format + MCP tool schemas ([ADR-0020](adr/0020-neutral-history-as-conversation-state.md)) | — |
| First API provider | Anthropic · Gemini · OpenAI-compatible | Decided for development: the operator's choice ([ADR-0021](adr/0021-providers-chosen-by-the-operator-claude-cli-included.md)); Haiku via the claude CLI first, DeepSeek via OpenRouter as fallback ([ADR-0022](adr/0022-haiku-first-during-development.md)) | Revisit with more users or stronger tasks |
| Choosing models by purpose | Tiers and skill frontmatter only · a router classifier (layer 4) · per-purpose profiles (conversation, writing tools, analysis) | Not chosen | When a task needs a stronger model than the cheap tier |
| Where nodes live | Cloud hosting · Pi · home PC | Edge in the cloud or on a Pi, claude CLI worker on the owner's PC | The P1 monthly bill |
| Sleep and wake-up | Always-on process · scale-to-zero | Always-on edge, workers may sleep | Same |
| Sandbox for scripts and MCP modules | Separate uid + Landlock · bubblewrap/nsjail · WASM | Proposed for host commands and skill scripts in [ADR-0039](adr/0039-command-sandbox.md): `no_new_privs`, a clean environment, Landlock and a PID namespace, fail closed | A spike in M3d on the owner's machine, Railway and a Pi |
| How long ended sessions (episodes) are kept | Forever, searched by `memory.recall` (today: the newest 500 per chat) · a retention: sessions older than N days are dropped after consolidation has read them | A retention after consolidation (owner, 2026-11-03): nothing is dropped that consolidation has not read, so a fact the owner never confirmed is still turned into one or kept; what was said by chance fades | M7 task 7.2, with the nightly consolidation; N is chosen there |
| Telemetry of edge nodes | Each edge to an OpenTelemetry collector over OTLP · Prometheus scrapes each edge · snapshots over the NATS bus, collected by the core | Decided by the owner (2026-11-03): over the bus, collected by the core, without JetStream for metrics (R-OBS-9). A collector needs one more link and one more service for every edge; the bus is already there. An outside collector can later be fed by the core | ADR at the start of P2, with the protocol |
| Home Assistant | WebSocket API · HA MCP server · MQTT discovery | Deferred to P3 | HA in Docker for testing |

Each question becomes an ADR (status Proposed) when work on it starts and is
accepted when decided; see [docs/adr](adr/README.md). Already decided: one
process per node ([ADR-0004](adr/0004-one-binary-with-roles.md)); building
from scratch ([ADR-0003](adr/0003-build-from-scratch.md)); the module ABI
([ADR-0011](adr/0011-module-abi.md)).

## 12. Risks

The main risk is scope creep. A distributed network with a router is already
a platform, not a weekend project. The antidote: phases with hard gates and
the "Not now" list.

| Risk | Signal | Response |
|---|---|---|
| Scope creep | A task appears that is not on the current phase's list | The idea goes to "Not now"; the phase does not grow |
| Anthropic or Claude Code CLI terms change | `claude -p` breaks or access rules change | claude CLI is just one worker; the system works without it |
| Open-source burden | Issues eat development time | 0.x, "no guarantees" in the README, public release no earlier than P3 |
| Self-modification breaks the bot | A regression scenario fails | Automatic git rollback, admin alert |
| Tokens cost more than the saved RAM | Hosting + API cost more than the current setup | Cheap model for routine, subscription worker for heavy tasks, daily limit |

## 13. Stop criteria and decisions

If any criterion fires, we stay on the current setup and count the R&D as a
successful study:

- P1 is not closed within 4 weekends;
- after P1 the bill is higher than the current setup with a memory cap;
- the bot has noticeably got worse and it is not fixed within 2 weeks.

Decisions, with the options considered and the reasoning, are recorded as
ADRs in [docs/adr](adr/README.md).

## 14. Not now

Ideas that come up along the way and are outside the current phase go here.
Reviewed at each gate.

- Classifier and learning in the router (P2 / P3).
- Channels other than Telegram and MQTT; HTTP API; web UI.
- Compiling flows into Home Assistant automations.
- CBOR and microcontroller adapters.
- An A2A endpoint on the core for external agents (after P3, ADR-0013).
- Bot features contributed by tools and modules: commands, menus and
  buttons declared in the manifest in a channel-neutral form; a press is a
  `tool.call` through policies; confirmations, access and the bot token stay
  in the transport (P3, [ADR-0017](adr/0017-extension-points.md)).
- Telegram Mini Apps (web pages inside the chat): an HTTP server and
  `initData` validation in the core; a web UI, excluded from 0.x (ADR-0017).
- Packaging and install paths for other users: a Docker image, a Railway
  template, a Helm chart, a systemd unit for a Pi, `ganglion init` (closer
  to the public release in P3).
- First-run onboarding without hand-written ids: the node prints a one-time
  pairing code, and the first person to send it to the bot becomes the
  admin (the same idea as node enrolment with a one-time token, 9.3).
- An encrypted secret store on the node's key, behind the secret source of
  D-3; secrets set and rotated without a restart.
- Telegram, beyond formatting, typing and replies (ADR-0025). Each item
  is to be checked against the Bot API docs of the day:
  - a reaction on a message the agent has seen, useful in groups when
    someone addresses the agent directly;
  - streaming answers as they are generated (needs streaming providers
    and must fit the outbox's delivery guarantee);
  - per-group behaviour and limits in the node config.
- Voice messages: understanding them, later, as a separate module or
  model (owner, 2026-11-02).
- Hooks and pipelines: an internal n8n (owner, 2026-11-03).
  - **Hooks** are module triggers on the node's own events, beside `cron`
    and `tool` (7.10's module ABI). Examples: before or after a tool call,
    after a turn, a file arrived.
  - **Sandbox.** A hook is a WASM module with the rights its manifest
    grants, never a shell script. A shell script would walk around the
    policy (R-POL).
  - **A hook that can refuse a call** is part of the security policy, so
    it needs its own ADR.
  - **Pipelines** are chains of modules and tools, where one step's output
    is the next step's input. The flows we put off: "complex task flows"
    (owner, 2026-11-03).
  - **A visual editor** connects the blocks on a canvas. It is a web UI,
    which 0.x excludes (above), so it comes after the public release.
  - Natural places: M4 for module triggers, P3 for flows the model writes.
- Reading pages that build their content with JavaScript: `web.fetch`
  reads the HTML the server sends, so a page rendered in the browser
  comes back empty. It needs a headless browser, too heavy for the core:
  later, as a job for a worker node, behind the same address checks and
  limits as `web.fetch` (owner, 2026-11-03, ruling R44).
- The bot's language as an install-time setting: the fixed texts, the
  confirmation questions and the toasts in the operator's language. In
  M3b they are English; only the command hints follow the client's
  language (English, Russian, Ukrainian, ruling R45) (owner, 2026-11-03,
  ruling R46).
- PDF generation: deferred, since Cyrillic fonts add weight; replies
  carry pictures and documents as attachments first (R-DLV-9) (owner,
  2026-11-02).
- Importers from popular assistants (OpenClaw first): persona, memory,
  skills and settings turned into ganglion's workspace files, so their
  users can move over (R-MEM-3, ADR-0024).
- Audiences and several agents: a "family mode" with its own agent or
  settings for a group of people, per-topic setups in Telegram forums,
  sub-agents and parallel agents. Until there is a public repository
  with install docs and settled processes, a node is one operator, one
  agent, one bot (owner, 2026-11-02).
- A decision model as a tool of the agent core: the owner's pick is
  **Jev** by TypeSafe ([typesafe.ai](https://typesafe.ai/),
  [docs](https://docs.typesafe.ai/)), a "System One" model.
  - **What it answers:** typed questions about a given state (text or
    JSON), without generating text. Three primitives:
    - **Noul:** is a statement true, 0 to 1;
    - **Score:** a rating on a rubric;
    - **Choice:** an option from a list.
  - **What it returns:** probabilities and a confidence. Several questions
    go in one request and are evaluated in parallel.
  - **Access:** its own API in early access, not OpenAI-compatible, so it
    needs its own provider type. Pricing on 2026-10-28: $42 per billion
    input tokens.
  - **When it runs:** only when such a model is in the catalogue and the
    operator allows it.
  - **Likely uses:**
    - the router's classifier (layer 4, P2);
    - a cheap pre-check before the reviewer model (7.6);
    - yes/no and rating checks inside skills and policies.
  - Other decision models seen in October 2026: Strands Decider 2B (AWS,
    open), the Decision 3.0 family, Liquid AI's d1.

  Noted 2026-11-02.
- Reading files and pictures sent to the agent: storage with accounting
  and a TTL (R-CMD-8), models that read pictures and PDFs, files as
  external data (a planted PDF is a classic injection path), and checks
  with M3d's commands (`file`, an antivirus). Its own brainstorm after
  M3d; until then R-DLV-10 (owner, 2026-11-03).
- Plans that approve other tools' calls, not only host commands
  (R-CMD-7) (2026-11-03).
- A turn cut short by a restart, seen and cheaper: today it reruns from its
  start, silently (R-DLV-3, ADR-0046). Two separate ideas:
  - tell the person "I'm continuing your interrupted request" when a turn
    starts over (`Lease.Interrupted` already says so), at most once per
    message; the OpenClaw gateway does this (owner, 2026-11-04);
  - keep the turn's finished model calls and tool results between steps, so
    a rerun goes on from where it stopped instead of paying for the whole
    turn again. It needs its own ADR: the kept messages hold outside content,
    and a tool that ran may have changed the world since.
- Managing the cloud a node runs in (Railway services, volumes, deploys)
  through that cloud's MCP server, with ganglion as an MCP client: P3 in
  the M3b and M3c design; earlier if the owner asks (owner, 2026-11-03).
