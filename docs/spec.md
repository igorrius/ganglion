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

ganglion is an open-source distributed agent network in Go for self-hosted
and edge/IoT setups. The name means a nerve node: reflexes are handled
locally, without the "brain".

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

**Core hypothesis:** a regular task runs as a WASM module without a model and
calls the model only on exceptions. Tested in P1 on the real Deye battery
monitor.

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

## 5. Architecture

### 5.1. Roles, not machines

One Go binary; roles are enabled with `--roles core,edge,worker`. A single
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
| claude CLI worker | worker | Tasks on the owner's subscription: write a module, analyse logs | unmodified `claude -p` | P1 |
| Tools and MCP | core, edge | `memory.*`, `schedule.*`, `message.send` in the core; `device.*` on the edge | MCP stdio / HTTP | P1 |
| WASM runtime | edge | Running modules with host functions from the manifest | wazero, own ABI (ADR-0011) | P0 (spike), P1 |
| Action policies | core defines, edge enforces | Autonomous / confirm / forbid, limits, `ttl`, audit | declarative config | P1 (base), P2 |
| Scheduler | core, edge | Crons, catch-up after downtime; on the edge, modules run without a link | `cron_runs` table | P1 |
| Memory | core | Facts, skills, semantic search; slices for edges | markdown in git; embeddings | P1 (files), P2 |
| Core ↔ edge protocol | all | Registration, calls, events, modules, policies, context | envelope + MCP over NATS | P2 |
| Bus | all | Delivery, buffering during outages, module object store | NATS: leaf node + JetStream | P2 |
| Observability | all | Turn trace: input, route, tools, cost, reply | slog + `turns` table, later OpenTelemetry | P1 (logs) |

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
    Caps() ModelCaps // tools, vision, context, local, price, latency
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

type Router interface {
    // Ordered list of "provider + model" candidates.
    Route(ctx context.Context, task TaskReq) ([]Candidate, error)
}

type Store interface { /* history, turns, cron_runs, allowed, module KV */ }
```

## 7. P1 subsystems: requirements

### 7.1. Guaranteed delivery

Guarantee: **at-least-once + idempotency.**

- **R-DLV-1.** An incoming event is first written to `inbox` with a unique
  `idemKey`; duplicates are rejected by a unique index.
- **R-DLV-2.** The Telegram offset advances only after the write to `inbox`.
- **R-DLV-3.** An actor takes an event under a lease (10 min by default). If
  the process dies, the lease expires and the event is processed again.
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

### 7.4. Providers and model access

- **R-PRV-1.** The first client is OpenAI-compatible Chat Completions: one
  client covers a cloud provider, Ollama, OpenRouter and the like.
- **R-PRV-2.** The native Anthropic Messages API is the second client, added
  when needed.
- **R-PRV-3.** Tools are described in MCP format; the core translates them
  into the specific API's schema.
- **R-PRV-4.** The claude CLI worker runs an **unmodified** `claude -p`. It
  suits standalone tasks (write a module, analyse logs), not conversation:
  `--resume` does not work with history kept by the core.

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

Routine work (conversation turns, crons, consolidation) goes to a cheap model
over an API; the subscription is for the owner's heavy tasks.

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

Target resources (estimates, measured in P0):

| What | Memory | Note |
|---|---|---|
| Runner with no modules | 15–25 MB RAM, 20–30 MB binary | Go + wazero + NATS client |
| Embedded leaf node with JetStream | +10–30 MB RAM | Depends on buffer size and stream count |
| One module | As the manifest allows, usually 1–16 MB | |
| Minimum hardware | Pi Zero 2 W (512 MB, arm64) | |
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
- **R-POL-5.** Kill switches are KV flags per action/device plus a global
  one; toggled by a Telegram command.

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
  procedure, commit and re-index embeddings.
- **R-MEM-2.** A dislike or a correction in chat flags the turn;
  consolidation handles flagged turns first.
- **R-MEM-3.** Memory is markdown files compatible with the current bot
  (migration without conversion).
- **R-MEM-4.** P1 uses files; embedding search and edge slices are P2.

### 7.8. Security and operations (MVP)

| Threat | Measure | Phase |
|---|---|---|
| Prompt injection | Confirmation of side effects; external text is data | P1 |
| Secret leakage | The model never sees secrets; modules get them via `secret.get` / env per manifest | P1 |
| A stranger messages the bot | `allowed` table with roles: the admin installs modules, others only ask | P1 |
| Infinite loop | R-AGT-4 | P1 |
| Exhausted limits | Crons and consolidation on a cheap model; usage counter in `turns` | P1 |
| Self-modification broke something | Regression scenarios (question → expected tool call) after every change | P3 |
| Lost volume or corrupted DB | Daily backup of SQLite (`VACUUM INTO`) and the memory git repo to S3 or a private remote | P1 |

## 8. First flow: Deye and load shedding (P1)

Deye battery monitoring plus staged switching of Tuya smart plugs by battery
state of charge (SOC). A scaled-down reference scenario: sensor, rules,
physical action, notification — all without an LLM.

### 8.1. States

```
              SOC < 50% and grid down          SOC < 40%
 ┌──────────────┐ ───────────▶ ┌──────────────────┐ ──────▶ ┌──────────────────┐
 │ All on       │              │ Big boiler off,  │         │ Both boilers off │
 │              │ ◀─────────── │ kitchen one on   │ ◀────── │ (base load only) │
 └──────────────┘ SOC ≥ 60%    └──────────────────┘ SOC≥50% └──────────────────┘
                  or grid back                      or grid back
 In every state: data older than 20 min → touch nothing;
 respect a manual switch-on for 2 h; a kill switch per plug and a global one.
```

### 8.2. Rules

| Rule | Value |
|---|---|
| Thresholds | 50% — big boiler, 40% — kitchen boiler. In the flow config, not in code |
| Condition | Only while the grid is down. If the inverter drains the battery even with grid, drop this condition |
| Restore | +10% hysteresis: big at ≥ 60%, kitchen at ≥ 50%; or when the grid returns — one at a time, 1 min apart |
| Debounce | Each plug switches at most once per 15 min |
| Polling | 30 min with grid, 5 min without (at 5 kW, 10% of a 10 kWh battery is gone in ≈ 12 min). Check DeyeCloud rate limits in P0 |
| Data freshness | Older than 20 min — switch nothing, report it |
| Manual override | Someone switched a plug on by hand — automation stays off it for 2 hours and reports it |
| Kill switches | KV flag per plug and a global one; off means notifications only. Telegram command |
| Notifications | Silent message to the notifications topic on every switch |
| Escalation to the model | Tuya or Deye auth error, plug not responding, no data for over an hour |

### 8.3. Blocks

Three reusable blocks:

- `deye-reader` — reads SOC and grid status (today a Python script
  `deye.py` in the current bot; rewritten in WASM in P0);
- `tuya-plug` — on / off / state of a plug;
- `load-shedding` — the flow itself, the state machine from 8.1.

The bot still answers "how is the battery?" through the `deye.status` tool.

**Tuya access** (chosen in P0):

- **Cloud (Tuya OpenAPI)** — a project on the Tuya developer platform plus a
  linked Smart Life account; works from cloud hosting, like DeyeCloud.
- **Local (LAN protocol, device key)** — only from a runner at home, but no
  dependency on the cloud or the internet.

The flow matters precisely during a power cut. The cloud path works only
while the home internet is powered by the inverter; otherwise a home runner
with local access is needed — a natural reason for P2.

### 8.4. Checks before enabling

- **R-LS-1.** Replay of the SOC history (samples every 30 min already
  accumulate in `deye.sqlite3`) and synthetic discharge curves during an
  outage.
- **R-LS-2.** Invariants covered by tests: a plug never switches more often
  than once per 15 min; never switches on while SOC is below the threshold
  and the grid is down; does nothing on stale data.
- **R-LS-3.** The first week is shadow mode: the flow writes "would switch
  off the big boiler" but does not call Tuya.
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
| 1. Skills | Markdown instructions | no | not needed | Yes, P1 |
| 2. Scripts | Python/sh next to a skill | no | filesystem and network sandbox | Yes, P1 |
| 3. MCP modules | A separate process + manifest with permissions | no | process, limits, granted secrets | Yes, the main path, P3 |
| 4. WASM | `.wasm` in wazero | no | capability list | Yes, P1 by hand, P3 by the model |
| 5. Core rebuild | `go build`, binary swap | yes | none | **Never** |

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
| **P1 · All-in-one** | Core + runner in one process: Telegram, inbox/outbox, history, rule-based router, one API provider + claude CLI worker; Deye module and load-shedding flow in production after a week of shadow mode; migrate the family bot | Deye runs 2 weeks without the model; the bot replaces the current one and costs less |
| **P2 · Network** | Runner on a separate Pi; protocol v0 over NATS (leaf + JetStream, JWT/NKeys); desired state; watchdog; MQTT devices; policies on the runner; embedding memory; router classifier | Link loss without data loss; module update on an edge and rollback with one command |
| **P3 · Skill compilation** | The model writes contract, code and tests; build on a worker; signed one-click install; repair on `module.crashloop`; agent-written MCP modules; Home Assistant | The model compiled and updated a module on its own; public release 0.1 |

### 10.1. P0 · Recon — tasks and acceptance

| # | Task | Done when |
|---|---|---|
| 0.1 | Repository skeleton: `cmd/ganglion` with `--roles`, packages for the interfaces in section 6, CI (build + test + lint) | `go build ./...` and tests are green in CI |
| 0.2 | Rewrite `deye.py` in TinyGo → WASM; run it in wazero with `http.fetch`, `state`, `secret.get` | The module returns the same SOC as `deye.py` against the real API |
| 0.3 | The same through Extism; build the Deye reader with TinyGo, Go and Rust under both ABIs | [ADR-0011](adr/0011-module-abi.md) accepted: own ABI or Extism, backed by a 2 × 3 table of module size, call time and runner RAM |
| 0.4 | Read the MCP transport (Streamable HTTP) and A2A specs | Recorded what part of the 9.1 protocol they cover |
| 0.5 | Measure runner RAM (wazero + embedded NATS) on a Pi or in a container limited to 128 MB | Estimates in the 7.5 table replaced with measurements |
| 0.6 | Access to Tuya plugs: cloud or local key | A plug switches on/off from Go code; path chosen |
| 0.7 | Does the home internet stay up on the inverter during an outage; DeyeCloud limits for polling every 5 min | Answers recorded; they inform 0.6 and the P2 timeline |

### 10.2. P1 · All-in-one — scope and acceptance

Scope: sections 7.1–7.8 and 8 in full.

Acceptance:

- A test bot with its own Telegram token completes a turn: message → inbox →
  model → tools → outbox → reply; killing the process at any point loses
  nothing and duplicates at most one message (R-DLV-*).
- The router returns candidates from a skill's frontmatter and falls back on
  429 and timeouts.
- The Deye module runs on cron without the model; the load-shedding flow has
  passed a week of shadow mode and is enabled; R-LS-2 invariants are covered
  by tests.
- **Gate:** Deye runs two weeks without the model; the family bot has
  migrated and costs less than the current setup.

Testing uses a separate bot: two processes with the same token cannot
coexist. The family bot migrates at the end of P1; the current bot stays as a
fallback until the gate is passed.

## 11. Open decisions

| Question | Options | Leaning towards | Decided by |
|---|---|---|---|
| Language of model-written modules | TinyGo · Go · Rust · AssemblyScript | Not chosen; P0 numbers (ADR-0011) favour Rust or TinyGo over standard Go | Start of P3: how reliably the model writes and fixes each, plus the P0 numbers |
| Tuya access | Cloud · LAN | — | P0, tasks 0.6–0.7 |
| Worker protocol | NATS request/reply · gRPC · HTTP | NATS: one bus for events and tasks | Two-node spike in P2 |
| History and tool format | Own · Anthropic Messages · OpenAI | Neutral own format + MCP for tools | Two providers in P1 |
| First API provider | Anthropic · Gemini · OpenAI-compatible | OpenAI-compatible | Price and quality of a cheap model on real turns |
| Where nodes live | Cloud hosting · Pi · home PC | Edge in the cloud or on a Pi, claude CLI worker on the owner's PC | The P1 monthly bill |
| Sleep and wake-up | Always-on process · scale-to-zero | Always-on edge, workers may sleep | Same |
| Sandbox for scripts and MCP modules | Separate uid + Landlock · bubblewrap/nsjail · WASM | Not chosen | What works in a container and on a Pi |
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
- OpenTelemetry instead of slog + `turns`.
- An A2A endpoint on the core for external agents (after P3, ADR-0013).
