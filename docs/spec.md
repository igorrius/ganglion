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
8. **Memory and size are goals; the node runs on any machine it is placed on**
   ([ADR-0087](adr/0087-distribute-to-any-machine-and-drop-small-hosts-as-a-target.md)).
   Memory and binary size are weighed in every decision about dependencies,
   runtimes and module languages, and each milestone compares its numbers with
   the previous one (`make memory`, `tools/modbench`). No device is the aim:
   the network is distributed, and a node runs where the operator places it, on
   a small device, a large VM, a start-worker or a Kubernetes cluster. Go has no
   hard memory cap: a node sets a soft limit (`GOMEMLIMIT`, R-RUN-6) and the
   collector works to stay under it. A feature that needs more memory than a
   node has is allowed: it runs where the resources are, on a denser edge or a
   worker (P2), and a node without the resources runs without it. Where work
   runs is the operator's choice, not a rule of the code. Workers that host heavy model
   runtimes (claude CLI, Ollama) run where the resources are (ADR-0009).
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
10. **The repository holds the core, not anyone's modules** (owner,
    2026-11-14, [ADR-0083](adr/0083-the-repository-holds-the-core-and-no-users-module.md)). A module is what the node builds at a
    user's request. A module, a reader or a fixture for one person's device
    or account is not added to the repository; what the agent needs in
    order to write one is carried by neutral templates and by built-in
    skills, which are updated with the node.

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
| Tainted session | A session that external content (a web page, a search result, a forwarded message, a quoted or replied message not written by the owner or by a clean session, or a reply to a message in another chat) has entered; side effects in it need confirmation (R-POL-6) |
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
  Two kinds of message are progress, not the reply, and enter the outbox during
  the turn under a key from the turn, so a rerun does not send them twice: an
  answer overtaken by a later message (R-LIVE-5) and a plan (R-LIVE-8).
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
  In webhook mode nobody polls (R-DLV-18).
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
- **R-DLV-11.** A message with a table or at least two headings goes out as a Telegram rich message
  when `telegram.rich` is on, the default. One with buttons, an edit, an attachment, a fold
  or a superseded answer does not. The transport builds the message's blocks from an allowlist and never
  passes the model's text to Telegram's parser, so no button or media block can come from it. A refused
  rich message is sent the ordinary way, never lost
  ([ADR-0054](adr/0054-structured-replies-as-rich-blocks-the-transport-builds.md)).
- **R-DLV-12.** A rich message sent to the bot is read: its text comes to the agent as outside content,
  with or without a forward's origin, and one with nothing readable is still noted, never dropped silently.
- **R-DLV-13.** A reply tells the agent which message it answers, for the bot's messages and the
  sender's own, up to 90 days old, a task's report and a reminder included: a short pointer to a message
  of the current session, which the history already holds; the time, the source and the text, cut at
  about 2,000 characters, of any other. A forward, another person's message, a message in another chat,
  a message of the node's older than 90 days or one the node cannot find comes as outside content. In a
  forum topic, the topic's root, which Telegram names as the replied message of every message that is no
  explicit reply, is not a reply
  ([design](plans/p1-reply-context-design.md),
  [ADR-0055](adr/0055-a-reply-carries-the-replied-messages-origin.md)).
- **R-DLV-14.** Every message the node sends records its origin: the session that made it and whether
  that session was tainted; a reminder's also records its job (R-SCH-2). The origin of a reply to the node's message is read from its own outbox by any part's ref, never
  from the channel's copy. A failed lookup never fails or delays the turn: the channel's copy is then
  outside content. Messages stored outside a turn (alerts, notifiers, the give-up reply) record none
  and count as tainted.
- **R-DLV-15.** A module's message (R-RUN-16) records the module that wrote it. A reply to it tells the
  agent the module's name and the time as the node's own words, with none of the message's text, the
  channel's copy or a selected quote, so it does not taint the session and the agent can read and
  rewrite the module (R-RUN-17). When the run that sent it is in the journal, the note adds its chain
  in the node's words: causes, times, host calls by kind, cells written (R-RUN-20). A name that could not be a module's makes the message one of an unknown
  source. A message the channel says the bot wrote and this node has no record of is said to be so
  (another node of the bot may have sent it, or it is over 90 days old); its text stays outside content
  ([ADR-0073](adr/0073-a-modules-message-names-its-module.md)).
- **R-DLV-16.** In a group chat the bot answers only when it is addressed, whatever Telegram's privacy
  mode is set to: the message mentions the bot, replies to a message the bot sent, or is a command that
  carries the bot's name (`/cmd@bot`). A bare command, the bot's name typed with no mention and a forum
  topic's root do not address it. Anything else in a group is dropped by the transport before the
  inbox: it is not stored, its text is not logged and it costs no model call. A private chat is not
  checked ([ADR-0076](adr/0076-telegram-groups-and-the-addressing-rule.md)).
- **R-DLV-17.** A group chat is answered only when it is listed under `telegram.groups` in the node's
  file, and then for every member, whoever they are. The first time the bot is addressed in a group
  that is not listed, or is added to one, the node says once in that group that it has no permission
  yet, and sends an admin, in their private chat, a card with the group's title and id and the buttons
  Allow and Refuse. Allow writes the group's line with the role `family` and the group works from the
  next message, with no restart. Refuse is remembered and the group is not asked about again. A
  request nobody answers lasts 7 days; at most 10 wait at once, and further ones are dropped and
  counted ([ADR-0076](adr/0076-telegram-groups-and-the-addressing-rule.md)).
- **R-DLV-18.** Telegram updates arrive by long polling or by a webhook, as `telegram.updates` says
  (R-CFG-11; owner, 2026-11-13,
  [ADR-0078](adr/0078-telegram-updates-by-polling-or-webhook.md)). In webhook mode the node registers
  the webhook itself at every start (`setWebhook`, one connection, pending updates kept) on its public
  listener. A request is accepted only with Telegram's secret header, compared in constant time; the
  secret is derived from the bot token and is in no file, address or log. An update takes the path a
  polled one takes, with the same `idemKey` (R-DLV-1), and `200` is sent only after it is in the inbox
  (R-DLV-2); a failed write is `500`, so Telegram sends it again. Updates that waited while the node
  was away are taken by polling before the webhook is registered, since Telegram's next retry can be
  two minutes off ([research](research/2026-11-13-telegram-webhook-on-railway.md)). Back in polling mode the node removes
  only a webhook it recorded setting itself; another process's webhook stays and is the 409 alert of
  R-DLV-8. When another process takes the bot from a node in webhook mode, the node alerts the admins
  and does not set its webhook again.
- **R-DLV-19.** A webhook that does not work must not cut the owner off from the chat. Before it
  registers, the node checks that its public address reaches this process; when it does not, it polls
  for this run and tells the admins why. While it runs, repeated delivery errors that Telegram reports
  with updates waiting (three readings a minute apart) make it remove the webhook, poll for the rest
  of the run and tell the admins; `/status` warns for the whole run.
  The node changes mode by itself in no other case.

### 7.2. Conversations, history and the agent loop

- **R-AGT-1.** One actor (goroutine) per conversation: one turn at a time,
  timeout, cancellation. A turn may take the messages behind it (R-LIVE-1).
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

Live turns ([design](plans/p1-live-turns-design.md);
[ADR-0050](adr/0050-additions-join-a-running-turn.md),
[ADR-0051](adr/0051-status-reactions-outside-the-outbox.md),
[ADR-0052](adr/0052-a-plan-is-delivered-during-its-turn.md)):

- **R-LIVE-1.** A turn started by a person's message takes the unbroken run of
  pending events directly behind it, oldest first, stopping at the first that does
  not qualify, so a later message never overtakes an earlier one. An event qualifies
  when it is a message in the same conversation from the same person, is not a
  command, has not expired, and the sender is within the daily limits. A schedule's,
  a press's or an expiry's turn takes nothing.
- **R-LIVE-2.** Additions enter at boundaries, before a model call. A running tool is
  never interrupted. No tool call starts while an addition waits, the first one
  included, and also when the turn has no step or time left to take the addition: on the
  API route the remaining calls get a paired result saying they did not run, and on the
  CLI route the call is refused and the run ends, or goes on to its answer when no new
  run fits.
- **R-LIVE-3.** A CLI run ended for an addition is not a failure, a fallback or a
  retry. The core starts a new run with the whole history. It adds an estimate of what
  it sent to the turn's tokens, since such a run reports none. What the ended run wrote
  is lost; its tool calls and their results are kept.
- **R-LIVE-4.** An addition is a message like any other: the speaker's line, outside
  text wrapped, files described (R-DLV-10). A forward or an outside reply taints the
  session before the next model call, and its web addresses do not become known. An
  addition grants nothing: it does not approve, change or cancel a parked call.
- **R-LIVE-5.** An answer the model wrote before an addition is not hidden. It goes out
  at once, marked as superseded, and stays in the history as written; the next call
  answers with the addition in view. So does text the model wrote beside tool calls
  that were skipped. Each transport shows a superseded message as it can: in Telegram,
  a collapsed quote.
- **R-LIVE-6.** Additions and further answers spend the turn's step limit and its
  clock, which they do not extend. When either runs short, no more are taken and the
  newest answer is the reply. The reply answers the newest message taken. `Complete`
  marks every taken event done in its one transaction; after a crash before it, nothing
  was marked and the turn runs again. A turn that fails after taking additions sends one
  failure reply, keeps the messages in the history and marks them done.
- **R-LIVE-7.** Code, never the model, sets a reaction on a person's message in a
  private chat, only after the access list, when work takes long: 🤔 the model works,
  👨‍💻 a tool ran, 👀 the message waits behind a turn. 👌 says at once that an addition was
  taken. The reaction comes off at the end. A quick answer shows none. Reactions are
  hints: no outbox, no retries, and a refusal or a `429` never delays a message.
- **R-LIVE-8.** For a result it has to put together (a report, a comparison, a selection)
  the agent shows a plan: `plan.show` and `plan.step`, one message per conversation, sent
  during the turn and edited in place with ⏳ ✅ ❌ ⏭ and an outcome line, and kept in the
  chat. A question that wants a fact gets an answer with no plan. A later step that ended
  closes the earlier ones nobody closed, and ⏭ means a line nobody reached or the model
  skipped. A step that lists a host command is ticked by the node when that command ends,
  ✅ on exit 0 and ❌ otherwise, whether or not the model calls `plan.step` (#38). It displays and approves nothing: policies decide. M3d adds its one press
  (R-CMD-7).
- **R-LIVE-9.** Work the node runs outside a turn is a background job: a module build
  (always) and a host command past its wait (R-CMD-4). Each job has one card in the chat,
  a message the node sends when the job starts and edits in place every 10 s without a
  model (`🔨 battery-alerts · build · 0:45 · crates built: 14`), ending as done, failed or
  interrupted by a restart; it carries only the node's words, the job's title and counts.
  The job's end resumes the conversation at once. Every turn of the conversation carries
  a node note listing its running jobs, not stored in history, and `/status` lists them
  (R-OBS-11). The chat stays usable meanwhile; only work on the same module waits (M4c,
  owner, 2026-11-10, [ADR-0069](adr/0069-background-jobs-with-a-live-card.md)).

### 7.3. Model router

Takes a task and returns an ordered list of "provider + model" candidates.
The skill declares its requirements in the `SKILL.md` frontmatter:

```yaml
model:
  purpose: check       # chat | check | compact | translate | code (R-RTR-6)
  tools: true
  context: short       # short | long
  vision: false
  privacy: local-only  # data never leaves for the cloud
```

| Layer | What it does | Phase |
|---|---|---|
| 1. Hard filters (tags) | Drops models without the required capabilities; `privacy: local-only` keeps local models only | P1 |
| 2. Rules and order | Daily budget, worker health and exhausted limits, the task's purpose (a turn → `chat`; the clean-context check → `check`; compaction → `compact`; writing modules and skills → `code`); candidates in the order the config lists them for that purpose (R-RTR-6). A scheduled agent turn is a turn: what must stay cheap is a regular task that needs no model at all, which becomes a reflex (section 1, core hypothesis) | P1 |
| 3. Fallback chain | Error, 429, timeout or exhausted limit — take the next candidate | P1 |
| 4. Classifier | A cheap model estimates task complexity when the skill sets no `purpose` | P2 |
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
  rest of the turn is routed by that skill's `model` block; `privacy:
  local-only` lasts until the session ends, since the skill's data is then in
  its history ([ADR-0042](adr/0042-skills-and-scripts-before-the-sandbox.md)).
  In such a session the policy refuses every tool not declared to keep its
  data on the node, whatever a grant says
  ([ADR-0049](adr/0049-three-gaps-where-outside-text-and-private-data-cross.md)).
  Only the
  owner installs a skill; the agent may propose one by showing its text.
- **R-RTR-5.** A level-2 skill declares its script in the frontmatter:
  `script: {run, effects, env, timeout}`. `effects: read` runs by
  itself, except that in a tainted session a call with input asks, since the
  script may reach the network
  ([ADR-0049](adr/0049-three-gaps-where-outside-text-and-private-data-cross.md)); `effects: change` asks with a button and runs at most once per
  call key (R-DLV-6): after a crash it is not run again, and the owner is
  told its outcome is unknown; the output is outside content. The script runs without a shell, in its own process group,
  with an environment built from scratch plus the names it lists (the
  node's secrets never pass), a timeout of at most 120 s, and capped
  output. Until the sandbox of
  [ADR-0039](adr/0039-command-sandbox.md) (M3d) it can read what the
  node's user can read
  ([ADR-0042](adr/0042-skills-and-scripts-before-the-sandbox.md)).
- **R-RTR-6.** Models are chosen by purpose (M4,
  [ADR-0060](adr/0060-models-by-purpose.md)). The config lists the models
  once, then for each purpose an ordered list of `{model, effort}`:
  `chat` (turns and scheduled agent tasks), `check` (short yes/no
  decisions such as the clean-context check), `compact` (history
  compaction), `translate` (fixed texts into a language with no file,
  R-LANG-5), `memory` (the memory review, R-MEM-8, M5), `code` (writing
  modules and skills, used from P3). The
  purposes are a fixed list in code; an unknown one fails the start.
  `check`, `compact` and `translate` use `chat`'s list when absent;
  `memory` uses `check`'s; `code` has no default. The
  order is the preference; the next entry is taken on an exhausted limit,
  429 or a timeout. `effort` is passed to providers that take one; a
  provider that cannot take an effort set in the config fails the start.
  Over the daily budget a turn keeps free and local models only, whatever
  purpose a skill moved it to; the calls that are not a turn's own steps
  (compaction, the clean-context check, translation) are housekeeping and
  are not cut.
  A skill may ask for `chat`, `check`, `compact` or `translate`; `code`
  waits for P3. Tiers (`smart`, `cheap`) and "cheaper first within a
  tier" are gone.

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
  ([ADR-0036](adr/0036-mcp-bridge-over-a-per-run-unix-socket.md)),
  served by an own minimal MCP server
  ([ADR-0037](adr/0037-own-minimal-mcp-server.md)).

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
| `notify` | A message to the owner's private chat, or to the destinations the module's line names, within a daily limit (R-RUN-16, R-RUN-18) |

Example manifest (M4 form,
[ADR-0062](adr/0062-cells-owned-by-modules.md)):

```yaml
name: demo-reader
version: 3
triggers:
  - cron: "*/30 * * * *"
  - tool: status             # the agent sees it as demo-reader.status
capabilities:
  http.fetch: [api.example.com]
  secret.get: [DEMO_APP_ID, DEMO_APP_SECRET, DEMO_EMAIL, DEMO_PASSWORD]
  state: true
cells:
  report:                    # the module's own cells: demo-reader.soc, ...
    soc:    {kind: sensor, about: "Battery state of charge, %"}
    grid:   {kind: sensor, about: "Grid present: true/false"}
    sample: {kind: sensor, about: "The whole reading, 12 fields"}
  read: []                   # other modules' cells, by full name
config:
  inverter_sn: {type: string, required: true}
limits:
  memory: 32MiB
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
  glue exists for Go/TinyGo (`pdk/guest`) and Rust
  (`examples/modules/reader-template/src/guest.rs`); a new
  language needs its own small glue.
- **R-RUN-6.** Every node sets a soft memory limit (`GOMEMLIMIT`): the
  operator's own `GOMEMLIMIT` when set, else from its config, else about
  80% of its container's memory limit (cgroup v2
  `memory.max`, v1 `memory.limit_in_bytes`), else none. Edge JetStream streams
  have explicit size limits, so a long outage cannot grow memory or the
  backlog without bound (task 0.5,
  [ADR-0056](adr/0056-small-hosts-as-a-soft-target.md)).
- **R-RUN-7.** A module can be a tool of the core: the agent calls it
  through the same policies as any tool. The first one is `html2md`
  (7.11), a pure function granted no host functions, embedded in the
  binary (M3b). M4 extends the path to modules the agent builds
  (`demo-reader.status`, R-RUN-9).
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

- **R-RUN-9.** A module of the owner's system (`local/…`) has one way in:
  the agent builds it from source in `workspace/modules/<name>/` and the
  owner approves the install; the node never accepts a ready `.wasm` from
  a file, a URL or a chat (M4,
  [ADR-0061](adr/0061-local-modules-built-from-source.md)):
  - builds go through one `Builder` interface (`Can`: which languages it
    builds; `Build`: source → one `.wasm` and a log). The node itself is
    the builder unless a worker marked `builder` exists (P2); the node's
    builder runs each build in the command sandbox (7.12) with a
    temporary target folder removed afterwards, the only folder the
    build may write, and kills whatever the build left running
    ([ADR-0064](adr/0064-builds-write-only-their-temp-folder.md));
  - `module.build` and `module.install` are the agent's tools, and
    `module.status` shows the agent what the owner's `/modules <name>` card
    shows (state, version, health with the node's hint, runs and the last
    failure, the declared settings with the owner's values, a secret-like
    or long one as `<set>`), never a text the module wrote; its answer is
    outside content only when it shows a setting's value as text, not for a
    number, a boolean or a hidden value
    ([ADR-0075](adr/0075-reading-a-modules-state-keeps-the-session-clean.md)); the install
    window lists the network, secrets, cells created and read, triggers and
    limits, and a version that widens its rights says so on its own line;
  - a build that is not already in the module's journal must carry a
    manifest `version` above every version the node has had of the module;
    otherwise the plan and the install refuse it and name the number to
    set, so two builds never share a number (owner, 2026-11-11);
  - the install checks the name, the secrets (none of the node's own: the
    bot token, the providers' and the search keys), the required config,
    cell ownership, and, in a throwaway instance before anything changes,
    that the module loads and that its `health` passes; a new version that
    then does not start is rolled back to the one it replaced;
  - the language follows [ADR-0063](adr/0063-module-language.md): Rust by
    default, TinyGo for existing Go code, standard Go only when TinyGo
    cannot.
- **R-RUN-10.** Every version of a module stays in its journal: who built
  it, the note "why" (the agent's or the owner's words, never the module's),
  the workspace commit it was built from, and its runs, failures and
  exceptions. `/modules <name>` shows the journal from the module's start;
  `/modules config|off|on|rollback|remove` act without a restart.
- **R-RUN-11.** Modules own cells. The manifest's `cells.report` declares
  them; the install creates them as `<module>.<name>` and records the
  owner; a module writes only its own cells (`cell.report` names a cell
  without the prefix) and reads others' only when `cells.read` lists them
  by full name. A removed module's cells stay, read-only. A module's tools
  take the same prefix (`demo-reader.status`). The agent reads any cell
  (`cells.list`, `cells.get` with a condensed history) and never writes a
  sensor; the owner sees cells with `/cells`, without a model.
- **R-RUN-12.** The runner in the node runs a module on its cron lines
  (scheduler jobs of the kind "module", no model involved) and as an agent
  tool, one instance at a time; calls the optional `init` on load and
  `health` at install and on the module's card. Five failures within an
  hour disable the module and raise `module.crashloop`. A failure of a
  run nobody watched (a cron run, `init` at load) whose kind the
  manifest's `escalate` lists, and `module.crashloop` always, reaches the
  agent as a turn in the owner's private chat, once per kind per active
  version until a successful run clears it (a new version, a rollback or
  turning it on clear it too, and a restart does not repeat it); the model
  sees the kind, the node's facts and the node's hint for the kind, never
  the module's text. A failed tool call the agent made in a turn is not
  escalated: the turn has the node's words for it already (owner,
  2026-11-10); it still counts for the crash loop and the journal, and a
  successful run of either kind clears the episodes. Any reload that
  starts the module, `/modules config` included, also clears its open
  episodes.

- **R-RUN-13.** What a module returns to the model (its tool's output,
  its cells' values) is outside content only where it is a string: numbers,
  booleans and null never taint a session; any string taints it like a
  fetched page ([ADR-0049](adr/0049-three-gaps-where-outside-text-and-private-data-cross.md)).
  A cell's `about` is shown to the owner (`/cells`), never to the model,
  which gets the cell's name and kind (owner, 2026-11-09).

- **R-RUN-14.** The manifest declares the shape of each tool's output and
  the type of each cell's value; the node checks every answer and every
  `cell.report` against it. A mismatch is a format error: the answer
  reaches neither the model nor the cell, the run counts as a failure,
  and `module.format` reaches the agent with the node's own description of
  the mismatch (field, expected and actual type, never the value), as
  context to revise the module (owner, 2026-11-09,
  [ADR-0062](adr/0062-cells-owned-by-modules.md)). Every failure the
  model sees, a tool call's or an escalation's, carries a fixed hint per
  kind from the node's texts: `auth_failed` (the service refused the
  module's login: check its secrets and settings with `module.status`),
  `stale` (the data source has not updated), `module.format` (a defect in
  the module), `module.exception` (the node's log has the details), a
  timeout and a trap (owner, 2026-11-10). The module's own error text goes
  to the node's log on every failed run, whatever the verdict.

- **R-RUN-15.** A module can run when cells it listens to change
  (`triggers: - cell: [demo-reader.soc, …]`), with no model (M4c,
  [ADR-0065](adr/0065-cell-triggers-through-the-inbox.md)). Every cell a
  trigger names is in `cells.read`; a module never listens to its own
  cells, and the install and a rollback refuse a loop between modules. When a run that
  wrote cells ends, the host puts one `module.trigger` event per listening
  module into the inbox, keyed by listener, producer and run, so it
  survives a restart and is never doubled; the node routes it to the
  module host. The listener reads the values with `cell_get`. The chain's
  depth travels in the event, and above 4 the host stops it and tells the
  owner. Runs from triggers are unattended, supervised and escalated like
  cron (R-RUN-12). The window says "Reacts to: …"; the cards show the
  chain both ways ("Reacts to", "Feeds").
- **R-RUN-16.** A module granted `notify: {per_day: N}` writes plain text
  to the private chat of the admin who installed it, or to the
  destinations its line in the node's file names (R-RUN-18), through the
  outbox (M4c, [ADR-0066](adr/0066-notify-capability.md)). The node signs the
  message with the module's name and running version under the text, apart from it (a rich
  message's footer under a divider; the last line in italics where there
  is none,
  [ADR-0074](adr/0074-a-modules-name-goes-under-its-message.md)), escapes
  Markdown, adds no buttons or previews and cuts the text at 1000
  characters. `N` is at most 100 a day in the node's time
  zone; over it nothing is sent, the module gets the code "limit" and the
  owner one node line that day. A repeated run sends nothing twice. The
  model gets a node note without the text (`[module] demo-alerts notified
  you at 14:30`), which does not taint; a reply to the message names the
  module the same way (R-DLV-15). The message records the run that sent
  it (R-RUN-20). The window says "Writes to you: up
  to N messages a day". Other targets wait in "Not now".
- **R-RUN-17.** The agent writes a module's source with `module.write`
  (one file in `workspace/modules/<name>/`, relative path without `..`,
  hidden segments, `target/` or symlinks, at most 256 KB a file, 16 MB and
  200 files a module, a workspace commit per write, no press) and reads it
  with `module.read` (M4c,
  [ADR-0067](adr/0067-agent-writes-module-source.md)). In a tainted
  session `module.write` asks the owner first, with the file and how many
  of its lines are new or changed, and a file written on that press is not
  the node's own: the folder answers as outside content until a clean
  session writes the file again; a tainted call that was not pressed is
  refused ([ADR-0075](adr/0075-reading-a-modules-state-keeps-the-session-clean.md),
  R-POL-6). The node records each module
  file's digest as its own tools left it (`module.import` of an example,
  `module.write` from a clean session); a build or a read of a folder that
  matches its record does not taint, and the build then keeps only rustc's
  diagnostics about the module's own files. Any other change to the folder
  makes those answers outside content again (R-RUN-13). `module.build` runs
  in the background (R-LIVE-9): it answers at once, one build runs at a
  time per node, and the build's answer comes back as a note when it ends,
  at the depth of the call that asked for it; the module cannot be built or
  written again until then, and a conversation may build one module at most
  5 times in a row before a person writes there again (owner, 2026-11-10,
  [ADR-0070](adr/0070-builds-in-a-row-instead-of-depth.md)). They are clean only
  while the module's `Cargo.lock` lists, besides its own package, only
  crates.io crates that one of the node's examples locks with the same
  version and checksum; a missing or unreadable lock, or any other
  dependency, keeps them outside content
  ([ADR-0068](adr/0068-vouch-for-module-source-per-file-and-vetted-crates.md)).
- **R-RUN-18.** Where a module's messages go is set in the node's file,
  not in the module: its line may list `notify_to`, names of destinations
  (R-CFG-12), and every message goes to each of them; `owner` is the
  private chat of the module's owner and the default. The module does not
  know the list. A name is resolved when a message is sent. The daily
  limit counts messages, not deliveries; the node's own lines about a
  module go to the owner only. A message to another conversation than the
  owner's chat leaves no note in that conversation's history. A name that
  does not resolve sends nothing there and is counted
  ([ADR-0079](adr/0079-a-modules-messages-go-where-the-nodes-file-says.md)).
- **R-RUN-19.** A module is tried on its examples right after its build.
  `examples.yaml` beside the manifest holds examples as steps that set
  cells, move virtual time and fire a trigger, with counted expectations
  (messages, cells reported, a failure) and canned HTTP answers. The real
  `.wasm` runs in a sandbox: an empty state, cells and messages kept in
  memory, no network, fake secrets, a virtual clock; nothing of the node
  changes. The model reads only the node's words (counts, the step that
  failed and why), never the module's text; the install window shows the
  owner what the module would send and write, and what it would do on the
  current cells. A failed trial refuses the build's install; a module
  without examples installs, and the window says it was not tried
  ([ADR-0090](adr/0090-a-module-is-tried-on-its-examples-before-install.md)).
- **R-RUN-20.** The node keeps a journal of each module's runs: the last
  200 per module, pruned on write; a run delivered again replaces its row,
  and a removed module's rows go with it. A row holds the version, the
  cause (cron; the agent's call of a tool, by name; a change of cells by
  another module's run, with the cells and that run), when, how long, the
  outcome and kind in the node's closed sets, the host calls by kind with
  counts (`http.fetch`, `cell.get`, `cell.report`, `notify`, `state.get`,
  `state.put`, `secret.get`, `config.get`; a denied call counts), the cells
  written and the messages sent; never an argument, a value, a text or the
  module's error. A run from a cell trigger copies the producer's row and
  its chain into its own when written, so the chain lasts as long as the
  run does. A module's message records the run that sent it (R-RUN-16).
  An admin sees it with no model: `/why` as a reply to a module's message
  shows the chain from the cron line or the agent's call to the message;
  the card of `/modules <name>` has a Runs button, and `/modules runs
  <name>` shows the last 10 runs. The agent gets the last runs in
  `module.status` and the chain in a reply's note (R-DLV-15). A cell id or
  module name is shown only where it fits the node's name pattern. A row
  that cannot be written is logged and counted; it never fails the run
  ([ADR-0092](adr/0092-a-bounded-run-journal-per-module-with-the-chain-copied-in.md)).

Resources, measured in P0 on amd64 under a 128 MB limit (task 0.5,
[research note](research/2026-11-01-runner-memory.md)); arm64 confirmation
on a Pi is pending:

| What | Memory | Note |
|---|---|---|
| Runner with one module | 17–38 MiB steady, 23–78 MiB peak at load | Rust lowest, standard Go highest; the peak is wazero compiling the module |
| Leaf node with JetStream | 18 MiB idle; 45 MiB with 117 MB buffered and `GOMEMLIMIT=40MiB` (80 MiB without) | Set a soft limit and stream size limits |
| Edge binary | ~9 MB runner + wazero today; ~25–30 MB with embedded NATS (estimate) | Cross-compiled for arm64 |
| One module | 161 KB (Rust) to 3.6 MB (Go) of `.wasm` | Memory per manifest limit |
| Target hosts | Any machine the operator places a node on: a small device, a VM, a start-worker or a cluster; memory and size are goals, not a device | Principle 8, ADR-0087 |
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
  admin only or two people. A question nobody answers closes itself when its
  `ttl` ends and folds like an answered one (R-POL-9). The outbox keeps where a
  delivered message went, which is how a later turn finds the question.

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
  command. The first switch is `tools.host_exec`, set by an admin's
  `/commands on|off`; the agent cannot set a `tools.*` cell.
- **R-POL-6.** A session is **tainted** once external content enters it: a
  web page, a search result, a forwarded message (a quote of another
  message and a reply to a message in another chat count too: the
  transport marks them and the agent wraps that text as data), a
  scheduled task's fetch. A reply to, or a quote of, a message the node sent
  taints only when the session that wrote it was tainted, or its origin is
  unknown (R-DLV-14, ADR-0055).
  - The taint is per session, not per turn: the content stays in the
    history and would steer the next turn.
  - It lasts until the session ends (`/new`, idle reset, R-AGT-6) and
    survives compaction. The compaction summary keeps external content
    marked as data, so an injection cannot come back as the agent's own
    words.
  - In a tainted session these need the owner's confirmation: any side
    effect, any send to anyone but the requester, any memory write, and a
    read script given input.
- **R-POL-7.** A **clean-context check** guards what the agent keeps. It
  uses a cheap model with no history, no tools and no tainted context (the
  "dual LLM" pattern; a first, narrow form of the reviewer model above).
  - **On write:** every memory entry is checked before it is committed: a
    fact about the owner, their home and devices (how they work, as the
    owner describes it, included; M5), or a hidden instruction? Imperative entries
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
  - A task may be granted the read-only `module.status` and `module.read`,
    never the tools that build, install, import or write a module
    (`module.build`, `module.install`, `module.import`, `module.write`;
    R-RUN-9, owner, 2026-11-10).
- **R-POL-9.** A question stays open, whole, until it is answered, expires or is
  replaced: a newer question of the same tool, for the same person in the same
  conversation, asked in a later turn, replaces one that still waits; the old call
  never runs ([ADR-0088](adr/0088-a-newer-question-replaces-an-older-one-of-the-same-tool.md)).
  Once answered, expired or replaced it **folds**, so the chat keeps the record of what was decided and
  stops being in the way:
  - one short line in place of its buttons, with the decision and its time in the
    node's time zone: "Approved, 21:09", "Approved, but it failed, 21:09",
    "Declined, 21:09", "Expired, 21:09", "Replaced by a newer question, 21:09"; while the call runs, "⏳ Approved,
    running…";
  - the question's text under the line in a collapsed quote, so nothing in it can
    pass for the line;
  - a second press shows the same line and the same time. The time of a press is
    stored with the call;
  - a question split into several messages only loses its buttons, since an edit is
    never split; the journal keeps the decision.

  A plan (R-LIVE-8) and a message with quick actions do not fold (owner, 2026-11-05).
- **R-POL-10.** The admin role holds only in the admin's private chat. In a group chat everyone, the
  admin included, acts with the group's role, so the node is never changed from a group and text
  others wrote in a shared history cannot steer the agent into an admin's action
  ([ADR-0076](adr/0076-telegram-groups-and-the-addressing-rule.md)).

The rest of the baseline is in
[ADR-0031](adr/0031-agent-security-baseline.md): no data out
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
| Semantic | Facts about people, home, devices, preferences | Topic files `memory/*.md` (R-MEM-6); embeddings in P2 | Every topic whole in the system prompt within a budget (R-MEM-7); semantic search in P2 |
| Procedural | How to do specific things | Skills: instruction + script + model requirements | Description in context, body on demand |

- **R-MEM-1.** Nightly consolidation is a regular cron with its own prompt:
  re-read the day's turns, extract facts into `memory/*.md`, fix
  contradictions, delete stale entries, propose a skill for a recurring
  procedure, commit and re-index embeddings. Archived sessions it has
  read may then be dropped after a retention (section 11, M7).
- **R-MEM-2.** A dislike or a correction in chat flags the turn;
  consolidation handles flagged turns first. Since M5 a correction is
  stored during the conversation (R-MEM-8); the flag for consolidation
  stays in M7.
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
- **R-MEM-6.** Facts are kept by **topic** (M5, owner, 2026-11-11,
  [ADR-0071](adr/0071-memory-topics-whole-in-the-prompt.md),
  [design](plans/p1-m5-memory-topics-design.md)). A topic is one file,
  `memory/<slug>.md`: a title and a one-line summary in the frontmatter,
  then a list of short facts. Each fact has an id that is stable inside
  its topic and records its date, its turn and whether its session was
  tainted. A correction replaces the fact it corrects. Limits are checked
  on write: a fact 300 runes, 50 facts in a topic. Files in the older
  shape, one fact per file, are moved into topics at start, in one
  commit.
- **R-MEM-7.** An admin's system prompt holds every topic whole, in the
  "known facts" data block, within `memory.prompt_budget` (characters;
  default 24,000). Over the budget topics fold, the one changed longest
  ago first, to a line with the title and the summary, and are read with
  `memory.read`. The same files give the same text.
- **R-MEM-8.** Memory is written during the conversation, from the
  owner's words only ([ADR-0072](adr/0072-who-writes-memory.md)).
  - **In the turn:** the core rules tell the agent to write a correction
    or a lasting fact the owner states with `memory.write`, under the
    fitting topic, in the same turn. A turn with nothing to remember
    makes no extra model call.
  - **The review:** a conversation with an admin's messages not reviewed
    yet is read once by a cheap model (purpose `memory`, R-RTR-6) after
    it has been quiet for `memory.review.quiet` (default 10 minutes), or
    at once when its session ends. It reads the owner's messages and the
    agent's replies as text, never a tool's result, and the topics; it
    returns at most five operations, which code applies. It is durable
    (a stored due time and an inbox event, once per span of messages),
    reads only the owner's private chat, is skipped for scheduled tasks,
    module events, family members and local-only sessions, and is
    switched off with
    `memory.review.enabled: false`.
  - Every fact from either path passes the clean-context check
    (R-POL-7). Messages of a tainted session are not written by the
    review: the owner gets one message listing the facts, with Remember
    and Skip (R-POL-6).
  - What the agent found out itself, from a tool or a page, is not
    stored here; it waits for consolidation (R-MEM-1).
- **R-MEM-9.** The owner sees every change. A write by the agent
  or a review that changed memory sends one note with the turn's
  messages, in the owner's language, listing what changed, with an Undo
  button that reverts those commits; the note then folds (R-POL-9). An undo that cannot apply
  says so and changes nothing. `/memory` (admin, private chat, no model,
  no note in the history) lists the topics; `/memory <topic>` shows one.

### 7.8. Security and operations (MVP)

| Threat | Measure | Phase |
|---|---|---|
| Prompt injection | Confirmation of side effects; external text is data; tainted sessions, the clean-context check and grants (R-POL-6–8); the `web.fetch` URL rule (R-WEB-2); [ADR-0031](adr/0031-agent-security-baseline.md) | P1 |
| Secret leakage | The model never sees secrets; modules get them via `secret.get` per manifest, from the encrypted store or the environment (R-CFG-9); secrets are redacted before the journal and logs, and the Bot API token is never logged | P1 |
| A stranger messages the bot | `allowed` table with roles: the admin installs modules, others only ask; in M3b a `family` member gets conversation with no tools and no memory, and the family bot migration (M7) decides family access | P1 |
| A group chat reaches the bot | Only a group listed under `telegram.groups`, connected by the admin's press, and only a message that addresses the bot (R-DLV-16, R-DLV-17); its members get the role `family`; nobody has the admin role in a group (R-POL-10); [ADR-0076](adr/0076-telegram-groups-and-the-addressing-rule.md) | P1 |
| Diagnostics expose the node | `ganglion mcp` opens no port: it runs over stdio and relies on the platform's authentication; read access to the data directory is read access to all history | P1 |
| A host command reads secrets or gains root | Commands run in a sandbox: no privilege gain, a clean environment, the node's environment, memory and secret files unreadable; root and anything that starts a process outside the sandbox are forbidden; the tool is off where the sandbox cannot be set up (R-CMD-1–3, [ADR-0039](adr/0039-command-sandbox.md)) | P1 (M3d) |
| The node's own files fill the disk | Everything the node creates is accounted for (what, size, age) and cleaned by age or size (R-CMD-8) | P1 (M3d) |
| Infinite loop | R-AGT-4 | P1 |
| Exhausted limits | Regular work as reflexes with no model where it can be; a model out of limits cools down and the next one in its purpose's list takes over; usage counter in `turns` | P1 |
| Self-modification broke something | Regression scenarios (question → expected tool call) after every change | P3 |
| A release that is not the project's becomes the node | A release is installed and started only with the project's signature, from GitHub's hosts, never older than the running version, and only at an admin's command; the agent has no tool for it (R-UPD-2–4, [ADR-0081](adr/0081-update-the-node-from-signed-releases-at-the-owners-command.md)) | P1 (M6) |
| An image that is not the project's is run as a node | The image is pushed only by the release workflow on a tag of a commit on `master`, with every action in its pushing jobs pinned by commit SHA; its index carries a provenance attestation a user checks with `gh attestation verify` (R-UPD-8, [ADR-0086](adr/0086-publish-the-nodes-image-to-ghcr-on-a-core-tag.md)) | P1 |
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
  `/metrics` on a local address (`metrics.addr` in the config, default
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
  `<data>` is the directory of the database; `log.dir` in the config
  moves the logs, and `off` keeps them on stdout
  only ([ADR-0043](adr/0043-logs-on-disk-with-an-own-rotator.md)).
- **R-OBS-7.** `ganglion mcp` is an MCP server over stdio for diagnosis
  from outside (M3c). It needs no running node and no model: it opens the
  database read-only and the log files, and offers read-only tools (search
  logs and the journal, recent failed turns, the effective configuration
  with secrets redacted, queue state). It is reached by running the
  command, locally or through `ssh`, `docker exec` or `kubectl exec`; it
  opens no port. Its results are marked as outside text, and its
  instructions say so
  ([ADR-0049](adr/0049-three-gaps-where-outside-text-and-private-data-cross.md)). The agent gets the same search tools, for admins only,
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
    second path from each edge: the bus stays the only link an edge needs.
  - **What is collected.** Only what is safe to collect: no content, no
    identifiers of people or conversations, and label values from closed
    sets.
- **R-OBS-10.** `/last` shows an admin what the last turn of this conversation did,
  in plain words and with no model (owner, 2026-11-05): when and how long, how it ended
  and why, the models tried, the steps, each tool call and how it was decided, the messages
  that joined it (R-LIVE-1), tokens and cost. Commands are not turns here. It joins no
  history. It names tools and never shows their arguments or results, which hold outside
  text and private data and which a message in a chat carries further; `turn.show`
  (R-OBS-7) is for those. A scheduled task runs in a conversation of its own, so `/last`
  in the chat does not show its runs.
- **R-OBS-11.** `/status` shows an admin how the node is doing now, on one screen and with
  no model (owner, 2026-11-08, [design](plans/p1-status-design.md)): a verdict first, then
  the node (the full version of ganglion, uptime, memory, the database and log sizes), the
  models (the ones the current session uses, each model's last answer and failure, the day
  against the daily limits and its spend), delivery (Telegram's polling, the queues) and
  what is off with its reason (skills, modules, failed turns, the scheduler). Every
  component contributes its own lines through one interface, so a new one shows up without
  a change to the command. A reason is a fixed text with parameters, never an error's text,
  which can hold outside content. It joins no history.

### 7.10. Scheduler and reminders

The scheduler moves up from M4 to M3b; M4 reuses it for module triggers.

- **R-SCH-1.** Crons run in the node's time zone (`node.tz` in the config, default
  Europe/Kyiv) and are recorded in `cron_runs`. After downtime each missed
  job runs once, not once per missed slot. A run's `idemKey` is the job
  plus the scheduled time.
- **R-SCH-2.** The agent schedules two kinds of jobs:
  - a **reminder**: a fixed text at a time;
  - a **task**: an instruction run as a full agent turn with tools.

  Both are created and listed in words; a list comes with buttons
  (Details, Delete, and Run now for tasks). Creating a task asks for
  confirmation, because it runs unattended and spends tokens, and fixes
  its grant (R-POL-8). A reminder, one-time or recurring, is set without
  a press in a session that is not tainted, and asks in a tainted one
  (R-POL-6); the reminder it sets is shown as its own message with
  Delete ([ADR-0089](adr/0089-set-a-reminder-without-a-press-in-a-clean-session.md)). Deleting the owner's own reminder or task
  likewise needs no press in a session that is not tainted and asks in a
  tainted one; the answer is one short "🗑 Off" line, and system and module
  jobs cannot be deleted at all. A recurring reminder's message carries
  Done, whose press deletes the job and says so in the message with no
  model turn. A reminder's message records its job (R-DLV-14), so a reply
  to it names the job to the agent. The list goes to the chat as cards
  only when the owner asked to see it; otherwise only the agent reads it
  ([ADR-0091](adr/0091-delete-the-owners-job-without-a-press-in-a-clean-session.md)). Each task has its own session, so its
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
[ADR-0057](adr/0057-commands-may-list-split-folders.md),
[ADR-0040](adr/0040-plans-as-approvals.md).

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

  A script runs only written out in the command (`bash -c`, `python3 -c`,
  `awk` and `sed` with their program); an interpreter given a file or its
  input is forbidden, since the press could not show it. `ps` and `top`
  are read only with name-only columns. Docker runs only the subcommands
  in the table.

  Moving an entry towards read widens rights and is the owner's decision,
  in configuration the agent cannot edit.
- **R-CMD-3.** Every command runs in a sandbox: it cannot gain privileges
  (`no_new_privs`), gets a clean environment, cannot read the node's
  environment or memory (the node marks itself not dumpable; `ps` still
  lists it, as it lists every process, owner 2026-11-08), and cannot read
  the node's data directory, its secret files, `.env` and `.env.*` files
  in any folder, or a default list of the owner's secret stores. A command may list
  every folder on the machine: listing is granted on the folders the sandbox splits around a
  denied path, and it reaches their whole tree, so the names inside denied folders are visible
  but not their contents (owner, 2026-11-08,
  [ADR-0057](adr/0057-commands-may-list-split-folders.md)); `sandbox.hide_names: true`
  hides them too. Below Landlock ABI 9, which reaches socket files,
  a command opens no Unix socket at all, so gpg-agent and the keyring are
  out of reach (`docker ps`, `systemctl --user` and `lpstat` with them);
  signals are not limited, so `kill` reaches the owner's processes
  (owner, 2026-11-08; [ADR-0039](adr/0039-command-sandbox.md)). The
  owner adds paths with `sandbox.deny`; `.env` files are found
  to three folders deep under home and the command's folder. If the node cannot set
  this up, or runs as root, `host.exec` is not offered and the node says
  why.

  Docker is one consequence of these rules: its command-line tool reaches
  its daemon over a Unix socket, so below Landlock ABI 9 Docker commands
  fail from the sandbox.
- **R-CMD-4.** A command longer than a short wait continues in the
  background as a job with a live card (R-LIVE-9); its completion resumes
  the conversation with the result at once.
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
  outcome (R-LIVE-8). A plan with no step that needs a press runs without one. A
  plan with such steps has one "Run" button that approves exactly the
  listed commands, argument for argument, in this conversation, until the
  plan ends or expires. A command that needs a press and is not in the
  approved plan does not run: it gets its own confirmation or a new plan.
  A command is its argv and its folder: approval of one is not approval of
  the same argv elsewhere. The approved plan's question becomes the plan's
  message.
- **R-CMD-8.** The node accounts for everything it creates on disk (what,
  size, age) and cleans it by age or size; `host.stats` reports it. The
  claude CLI work directory is the first case.

### 7.13. The language of fixed texts

What the node writes without a model reaches people who may read only their own language
([ADR-0053](adr/0053-fixed-texts-in-the-persons-language.md)).

- **R-LANG-1.** A person's language is, in this order: the one they chose, the last valid
  language their channel reported, English. It belongs to the person, not the conversation. A
  scheduled message follows its job's owner; a message to a group with no person behind it is in
  English. A fixed text is rendered when its outbox row is written (R-DLV-5).
- **R-LANG-2.** Fixed texts a person reads follow it: replies, toasts, labels, questions, folded
  lines, an admin's included. These stay in English: what a model reads, `/last`, `/metrics`,
  `/modules` and admin alerts. A number never changes a noun's form in a text, so no language needs
  plural rules.
- **R-LANG-3.** A person chooses by saying so in the chat; there is no command. The model calls
  `language.set` with a code from the table of native names, for the caller alone. In a session
  that has read nothing from outside it applies at once. In a tainted one it asks with a button, in
  the requested language with the same question in the current one under it. Until permissions exist
  it is offered to admins only (owner, 2026-11-06), and never in a scheduled run; the others follow
  their channel's language. What a person may do becomes a group of permissions in the configuration
  ("Not now").
- **R-LANG-4.** Per key, the first that has the text wins: an operator's file, a shipped file, a
  machine translation, English. Operator files live outside the workspace, and no tool writes
  machine translations.
- **R-LANG-5.** A listed language with no file is translated once in the background, lazily: only
  when the node is writing to a person in it (owner, 2026-11-06), and never holds a turn. The call has no tools and no history; its only input is our English strings and the
  language's name. Code accepts a text only with the source's exact placeholders, within its limit,
  and with no URL, mention or markup its source lacks. A pack missing an approval text, or that only
  echoes English, is refused. Each stored text keeps a hash of its English source, so a release
  translates only what changed. Attempts are capped per language per day. A language requested in a
  tainted session starts only after the press.
- **R-LANG-5a.** Command hints stay as R45 has them: shipped languages only, other languages see
  English hints.
- **R-LANG-6.** A person is told once per language, in that language, that the texts were translated
  by a machine and may be inaccurate. Every button that acts carries a symbol added by code, outside
  the translated text, and a question shows its command, address or text as is.
- **R-LANG-7.** A regular report follows its job owner's language (R-LANG-1), whoever writes it
  (owner, 2026-11-06). A system job written in code takes its texts from the catalogue and is given
  the owner's language. A system job that a model writes is told that language in its turn's lines.
  A task a person created keeps following its instruction, unless they chose a language. A model's
  verdict quoted in a report (a memory check's reason) stays in the words it was given, since the
  check is a measured security boundary.

### 7.14. Node configuration

One file, `ganglion.yaml`, holds everything an operator chooses (owner,
2026-11-09, [ADR-0059](adr/0059-node-config-file.md); decision D-3 of the
phase plan).

- **R-CFG-1.** Outside the file stay only the config path (`--config` or
  `GANGLION_CONFIG`, default `ganglion.yaml` beside the database), the
  database path (`--db`), the roles (`--roles`) and the secrets. No file
  at the default path means defaults; a path given with `--config` or
  `GANGLION_CONFIG` must exist.
- **R-CFG-2.** A field that takes a secret holds `env:NAME`; the node reads
  `NAME` or the file named by `NAME_FILE`. From M6 it may hold
  `store:NAME` instead, read from the node's scope of the secret store
  (R-CFG-9). A literal value in such a field fails the start. Module
  secrets are named by the manifest and never appear in the file.
- **R-CFG-3.** Parsing is strict: an unknown key fails the start, and an
  error names the path and the reason. `off` turns a thing off; a missing
  key means the default.
- **R-CFG-4.** `schemas/node.schema.json` describes every field (meaning,
  type, default, allowed values) and is kept in step with the code by a
  test ([ADR-0023](adr/0023-json-schemas-for-yaml-files.md)).
- **R-CFG-5.** The `GANGLION_*` variables that held settings before M4,
  `GANGLION_MODELS_FILE`, and the models file's `key_env` and `tier` are
  no longer read; a node that finds one, in its environment or pasted
  into the file, fails the start with the key that replaces it.
  `OTEL_EXPORTER_OTLP_ENDPOINT`, OpenTelemetry's own setting, is still
  read.
- **R-CFG-6.** Modules apply at once: install, update, rollback, config,
  off, on and remove. Every other section applies at the next start;
  `/restart` (admin) ends the node cleanly for its supervisor to start
  again.
- **R-CFG-7.** `node.memory_limit` sets `GOMEMLIMIT` (R-RUN-6). A
  `GOMEMLIMIT` the operator set, the Go runtime's own setting, wins over
  it.
- **R-CFG-8.** The `memory` section (M5): `prompt_budget`, the size in
  characters of the known-facts block (R-MEM-7), and `review` with
  `enabled` and `quiet` (R-MEM-8).
- **R-CFG-9.** The secret store (M6, owner, 2026-11-13,
  [ADR-0077](adr/0077-encrypted-secret-store-on-the-nodes-key.md)): secrets
  are encrypted rows in the node's database, so nobody creates a variable
  for each one.
  - A secret belongs to a scope: `module:<name>` or `node`. A module's
    `secret.get` reads its own scope, then the environment variable of
    that name; it never reads another module's scope or the node's.
    Removing a module removes its scope.
  - `ganglion secret set [--module <name>] NAME` takes the value on
    standard input, `secret rm` and `secret list` complete it, and a
    running node sees the change at the next read, with no restart.
  - Nothing shows a value again. `/secrets` (admin, private chat, no
    model) lists names, dates and the modules that name each, and removes
    one. A value the store hands out is redacted from the journal and the
    logs like one read at the start.
  - How the owner hands a value over from the chat is a separate decision
    (section 11).
- **R-CFG-10.** The store's master key is `GANGLION_MASTER_KEY` or
  `GANGLION_MASTER_KEY_FILE` when set; otherwise the file `master.key` in
  the data directory, which the node creates at its first start. A key
  that does not fit the stored secrets is never replaced silently: the
  node starts with the store locked (stored secrets read as unset, setting
  one is refused, `/status` says why) until the admin's `/secrets reset`
  drops them.
- **R-CFG-11.** The `http` section is the node's public listener: `addr`
  (`off` by default) and `public_url`, the HTTPS address the outside
  reaches it at. It serves plain HTTP behind a proxy that ends TLS,
  carries the Telegram webhook and a health route, answers `404` to
  anything else, and is never the metrics listener. `telegram.updates` is
  `polling` (the default) or `webhook` (R-DLV-18); `webhook` without a
  listener and a `public_url` fails the start. A change to any of the
  three opens or moves a public entry to the node: the agent proposes it
  on a card of its own, never from a tainted session
  ([ADR-0078](adr/0078-telegram-updates-by-polling-or-webhook.md)).
- **R-CFG-12.** `destinations:` in the node's file names conversations a
  message may be sent to: a private chat on the access list, a connected
  group (R-DLV-17) or one topic of a forum. The node remembers the titles
  of the topics it sees in connected groups, so the admin names a topic by
  its title. Destinations, a module's `notify_to` and disconnecting a group
  are changed by the admin in words, each change after one confirmation, and
  hold with no restart. The admin never has to name a destination:
  `message.send` and a module's route take a connected group or a topic by
  its key or by the link Telegram gives for it, and the node writes the
  destination behind it. A
  destination a module uses is not removed, and a group a destination
  points at is not disconnected, until what holds it is changed
  ([ADR-0079](adr/0079-a-modules-messages-go-where-the-nodes-file-says.md)).
- **R-CFG-13.** Every change to the node's file is recorded (M6, owner,
  2026-11-16,
  [ADR-0093](adr/0093-config-changes-through-the-agent-with-history-in-the-workspace.md)):
  each writer also copies the file to `config/ganglion.yaml` in the
  workspace repository and commits it with a reason and its source
  (`propose`, `rollback`, `module`, `destination`, `group`, `host`). At
  start a live file that differs from the last copy is committed with the
  source `host`. The live file stays at its path (R-CFG-1) and is the
  truth; tools that write the workspace refuse `config/`. A node without
  git works with no history.
- **R-CFG-14.** `config.propose` (admin, private chat) changes only these
  fields: `node.tz`, `node.memory_limit`, `log.level`, `telegram.reply`,
  `telegram.rich`, `telegram.reactions.after`, `telegram.reactions.dwell`,
  `agent.languages`, `agent.session_idle`, `agent.call_timeout`,
  `agent.turn_timeout`, `agent.limits.*`, `agent.search.mode`,
  `memory.prompt_budget`, `memory.review.enabled`, `memory.review.quiet`,
  `update.check` and `models.purposes`. Any other field is refused with
  the place that changes it: the host (secret references,
  `telegram.api_url`, `telegram.admin`, `models.providers`,
  `models.models`, `sandbox`, `http`, `metrics`, `log.dir`,
  `agent.workspace`) or its own tool (modules, destinations, groups,
  `telegram.updates`). A test keeps the list in step with the schema.
- **R-CFG-15.** A proposal is checked before its card and again on the
  press: strict parsing, the schema and the modules' checks. One that
  fails is refused with the path and the reason, and the file is not
  touched. The card shows each field's old and new value and the reason;
  one from a tainted session says so (R-POL-6). It expires as any question
  (R-POL-9). An applied change takes effect at the next start (R-CFG-6);
  the card then offers "Restart now".
- **R-CFG-16.** `config.history` lists the recorded changes with their
  source, reason and diff. `config.rollback` proposes, through R-CFG-15,
  the earlier values of the allowlisted fields a chosen change touched (the
  latest by default); the parts outside the allowlist are named with the
  place that changes them.

### 7.15. Updating the node

A merge changes nothing on a running node: a node moves to a new version
only at its owner's command, from a release the project signed
([ADR-0081](adr/0081-update-the-node-from-signed-releases-at-the-owners-command.md)).
A node is first installed from the image a core tag publishes (R-UPD-8);
other install paths are in section 14.

- **R-UPD-1.** A core release is a tag `vX.Y.Z` on a commit of `master`.
  CI builds the binary for `linux/amd64` and `linux/arm64`, writes
  `SHA256SUMS`, signs it with the project's Ed25519 key and publishes the
  files with a `release.json` that names the environment the binary needs
  (`env`). The public keys are embedded in the binary and published in the
  repository, so `openssl` checks a release too.
- **R-UPD-2.** `/update`, for an admin in their private chat and with no
  model, shows the running version and the newest release that fits the
  machine. `/update install vX.Y.Z` installs that release and restarts
  into it; `/update rollback` restarts into the previous version. The
  status and the daily notice (R-UPD-6) carry buttons that do the same for
  the version they name: a press is the confirmation, as writing the
  version is, by an admin at the time of the press (owner, 2026-11-15).
  `ganglion update` does the same on the host. No tool of the agent
  updates the node, and the agent never proposes it as a configuration
  change.
- **R-UPD-3.** A release is installed only when it comes from GitHub's
  hosts, its `SHA256SUMS` carries a valid signature of the project, the
  binary matches its sum, the version is newer than the running one, the
  platform is this machine's and its `env` is not above the image's. A
  refusal keeps the running version and says why in the node's own words.
- **R-UPD-4.** The node keeps the active and the previous release under
  `<data>/releases`, which no host command and no module can write. The
  binary the image or the package ships is never replaced: `ganglion run`
  starts, verifies the active release again and continues as it. A file
  that is not a signed release of the project is never run.
- **R-UPD-5.** A newly installed release gets two starts. A version whose
  components have run for two minutes is confirmed and the admins are
  told. One that is not confirmed after two starts gives way to the
  previous version with an alert. The database is copied before the
  switch, and the copy is put back when the failed version had migrated
  it.
- **R-UPD-6.** Once a day the node asks for the newest release and tells
  the admins when there is one; it never installs by itself.
  `update.check: off` in `ganglion.yaml` stops the asking.
- **R-UPD-7.** Nothing a release page says (its title, its notes) reaches
  a reply, the history or the model: the node shows the version, the date
  and a link.
- **R-UPD-8.** The same tag publishes the node's container image to
  `ghcr.io/igorrius/ganglion` for `linux/amd64` and `linux/arm64`, built
  from the repository's `Dockerfile` with the tag as the version
  `ganglion version` reports. It is tagged `X.Y.Z`; `X.Y` and `latest`
  follow the newest release of the line and of all, never a pre-release.
  Each platform's image is checked before it is published: the version,
  the toolchain as the node's user, and a start with the user guide's
  minimum configuration. The index carries a provenance attestation of
  the workflow
  ([ADR-0086](adr/0086-publish-the-nodes-image-to-ghcr-on-a-core-tag.md)).

<a id="8-first-reflex-deye-p1"></a>

## 8. First reflex: a reader (P1)

The first module of an owner's system is a reader: a module the agent
builds from source and installs with the owner's approval (R-RUN-9), which
asks an outside service on cron and writes sensor cells, with no model in
the loop. The agent answers "how is it now?" and "how did it go overnight?"
from the module's tool and the cells' history.

The project grew up on one such reader, of the owner's Deye inverter. It
is the owner's own module and is not in the repository
([ADR-0083](adr/0083-the-repository-holds-the-core-and-no-users-module.md),
principle 10); what it taught is kept in a template that names no device
and in the built-in skill `module-writing`. The requirements below were
R-DEYE-1..3 until 2026-11-14.

Load shedding, planned here until M4, left P1 (owner, 2026-11-09,
[ADR-0058](adr/0058-load-shedding-leaves-p1.md)): switching loads by
rules over sensor values is a flow of ready modules linked through cells,
which belongs with the flow engine (9.6).

- **R-RDR-1.** `reader-template` is a template in the repository
  (`examples/modules/reader-template`, Rust), not a part of the agent: the
  agent copies it into its workspace under a new name, changes the
  manifest and `src/logic.rs` (the request, the answer, the settings, the
  cells), builds and installs it. Every reader reports the cell `sample`,
  the whole reading, and the cells its logic names. The rules of R-RDR-2
  and R-RDR-3 are in a file the agent does not change and whose tests do
  not depend on the service. The templates are embedded in the binary,
  as the built-in skills are: each start writes them to `templates/` in
  the modules folder, replacing what was there, so a node's templates
  are those of its own version and an update of the node updates them.
- **R-RDR-2.** A reader polls on its cron. On HTTP 429 it waits until
  `Retry-After` has passed; a reading older than the reader's limit is
  reported once per episode (`stale`); a read the service refuses (HTTP
  401 or 403, or a secret that is not set) raises `auth_failed` and is
  never retried with the same secrets (task 0.7). To notice that a secret
  changed, the module keeps a 16-bit mark of its secrets, never the
  secrets.
- **R-RDR-3.** The tool `status` reads fresh under the same 429 rule; when
  the service cannot answer it returns the last reading with its age, and
  when there is no reading it answers the read's own failure, never an
  answer outside its declared output. `health` checks without network
  that the settings parse and that the secrets are set (owner's live
  check, 2026-11-10).
- **R-RDR-4.** The built-in skill `module-writing` says which template
  fits a request (a reader, a reflex, or both linked through cells), what
  to find out about a service before writing a reader, and how the
  modules are linked. It is embedded in the binary, so it is updated with
  the node.
- **R-LS-4.** Until the owner's bot migrates, his reader runs alongside
  `deye.py` and their readings are compared.

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
2. It writes the code and tests in the language
   [ADR-0063](adr/0063-module-language.md) picks: Rust by default for small
   system modules (HTTP, JSON, arithmetic), TinyGo when Go code already
   exists and TinyGo compiles it, standard Go only when TinyGo cannot and a
   rewrite does not pay. The model uses the `code` purpose (R-RTR-6).
3. The build goes through the `Builder` interface of R-RUN-9: the core
   node builds unless a worker marked `builder` exists. The build path and
   the install window come in M4; M4c adds the model writing the source
   (R-RUN-17, [ADR-0067](adr/0067-agent-writes-module-source.md)).
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

The compilation skill carries a mandatory instruction (owner, 2026-11-07), in
words close to these: "The system you are changing is what you run on as an
agent. Be careful not to break it: if it breaks, you stop working." It sits in
the skill itself, so it is in front of the model whenever it writes or ships a
module, and it adds to the measures above, which code enforces; it never
replaces them.

A second mandatory instruction (owner, 2026-11-09), in words close to
these: "A module does its one task fully and hands the system contract
data. It interprets what it fetched itself and returns only what its
tools and cells declare: numbers, flags, short fixed values, not raw text
from outside. Plain code full of if-else is fine; what matters is that
the model works after the module on clean, prepared data, or not at all."
This is the point of the platform: modules carry the regular work, the
model is called for what only a model can do. It also keeps the system
safe: a value that is not a string never taints a session (R-RUN-13).

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

Ready modules linked through cells: one module reports a cell, another
reads it (R-RUN-11) and acts. A flow is edited visually, in the spirit of
Node-RED, by a person or by the model, with the modules already written
by the model (owner, 2026-11-09). Its first step comes earlier, in M4c: reflex
modules that notify without a model (see 10.2).

#### First flow: load shedding (moved from P1)

Planned for P1 until M4 and moved here by
[ADR-0058](adr/0058-load-shedding-leaves-p1.md); kept as the first flow
to build on the engine. The sensor half is the Deye reflex (section 8);
the 30-minute polling would need the 5-minute cadence below again.

Deye battery monitoring plus staged shedding of loads by battery state of
charge (SOC). A scaled-down reference scenario: sensor, rules, action,
notification — all without an LLM.

The flow acts on **state cells**, not on devices
([ADR-0014](adr/0014-state-cells.md)): each load is a switch cell with a
*desired* state the flow sets and a *reported* state from its backend. In
P1 the loads use the `virtual` backend and a Telegram notifier observes
their changes; Tuya plugs become the backend when they are linked, without
changing the flow.

#### States

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

#### Rules

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

#### Blocks

Reusable blocks:

- `deye-reader` — reads SOC and grid status into sensor cells (done in P0 as
  a WASM module);
- load cells `load.big_boiler`, `load.kitchen_boiler` — switch cells; backend
  `virtual` in P1, `tuya` once the plugs are linked;
- a Telegram notifier — an observer of the load cells;
- `load-shedding` — the flow itself, the state machine above, which only
  reads sensor cells and writes the desired state of load cells.

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

#### Checks before enabling

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
| **P1 · All-in-one** | Core + runner in one process: state cells, inbox/outbox and Telegram, then the agent (history, rule-based router, one API provider, memory, skills, policies); the Deye module as its first reflex, built and installed by the agent; one config file; the agent changes the system's configuration with the owner's approval; migrate the family bot ([plan](plans/p1-all-in-one.md)) | Deye runs on cron without the model, its versions journaled; the bot replaces the current one and costs less |
| **P2 · Network** | claude CLI worker; runner on a separate Pi; protocol v0 over NATS (leaf + JetStream, JWT/NKeys); desired state; watchdog; MQTT devices; policies on the runner; embedding memory; router classifier | Link loss without data loss; module update on an edge and rollback with one command |
| **P3 · Skill compilation** | The model writes contract, code and tests; build on a worker; signed one-click install; repair on `module.crashloop`; agent-written MCP modules; Home Assistant | The model compiled and updated a module on its own; public release 0.1, with an announcement (the repository itself is open since P1, [ADR-0082](adr/0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md)) |

### 10.1. P0 · Recon — tasks and acceptance

| # | Task | Done when |
|---|---|---|
| 0.1 | Repository skeleton: `cmd/ganglion` with `--roles`, packages for the interfaces in section 6, CI (build + test + lint) | `go build ./...` and tests are green in CI |
| 0.2 | Rewrite `deye.py` in TinyGo → WASM; run it in wazero with `http.fetch`, `state`, `secret.get` | The module returns the same SOC as `deye.py` against the real API |
| 0.3 | The same through Extism; build the Deye reader with TinyGo, Go and Rust under both ABIs | [ADR-0011](adr/0011-module-abi.md) accepted: own ABI or Extism, backed by a 2 × 3 table of module size, call time and runner RAM |
| 0.4 | Read the MCP transport (Streamable HTTP) and A2A specs | Recorded what part of the 9.1 protocol they cover |
| 0.5 | Measure runner RAM (wazero + embedded NATS) in a container limited to 128 MB | Done: fits a 128 MB container ([research note](research/2026-11-01-runner-memory.md)); the Pi run for arm64 timings is dropped ([ADR-0087](adr/0087-distribute-to-any-machine-and-drop-small-hosts-as-a-target.md)) |
| 0.6 | Access to Tuya plugs: cloud or local key | Moved to P1: loads are state cells with a `virtual` backend until the plugs are linked ([ADR-0014](adr/0014-state-cells.md)) |
| 0.7 | Does the home internet stay up on the inverter during an outage; DeyeCloud limits for polling every 5 min | Done: [research note](research/2026-11-01-outage-connectivity-and-deye-limits.md); internet outlasts the inverter; DeyeCloud quota 100,000 calls a month, worst case uses ~10% |

### 10.2. P1 · All-in-one — scope and acceptance

Scope: sections 7 and 8 in full; load shedding left P1 in M4
([ADR-0058](adr/0058-load-shedding-leaves-p1.md)). Milestones, tasks, decisions and the
cut list are in the [P1 phase plan](plans/p1-all-in-one.md). The agent
milestone is split into M3a (talks), M3b (works with the world: tools,
policies, memory, the scheduler, web reading, the MCP bridge) and M3c
(module updates, skills, attachments, logs on disk, `ganglion mcp`), as
the [M3b and M3c design](plans/p1-m3b-m3c-design.md) sets out; the
scheduler moves from M4 to M3b. Between M3c and M3d came live turns
(R-LIVE) and smaller additions the owner asked for after using the bot: a
closed question that folds (R-POL-9), `/last` (R-OBS-10), fixed texts in
the person's language (7.13), Telegram rich messages (R-DLV-11, 12),
knowing which message a reply answers (R-DLV-13, 14) and `/status`. M3d
(host commands and plans, 7.12) follows them
([M3d design](plans/p1-m3d-host-commands-design.md),
[ADR-0038](adr/0038-host-commands-as-argv-with-classes-in-code.md)).
M4 brings one config file, models by purpose, modules the agent builds
from source, and cells owned by modules
([M4 design](plans/p1-m4-reflexes-design.md)); M4a and M4b closed
2026-11-10, ADRs 0059–0063 Accepted. M4c, reflex modules, follows: a
`notify` capability for modules and the agent writing module source in the
workspace, first `deye-alerts`, so routine reactions run without a model
(owner, 2026-11-10; [M4c design](plans/p1-m4c-reflexes-design.md),
ADRs 0065–0070, R-RUN-15–17, R-LIVE-9). M4c closed 2026-11-10: the agent
wrote, built in the background and installed `battery-alerts`, and its alert
reached the owner with no model call.

Acceptance:

- A test bot with its own Telegram token completes a turn: message → inbox →
  model → tools → outbox → reply; killing the process at any point loses
  nothing and duplicates at most one message (R-DLV-*).
- The router returns candidates from a skill's frontmatter and falls back on
  429 and timeouts.
- The agent builds the Deye module from source and installs it after the
  owner's press; it runs on cron without the model, writes its cells, and
  its versions and failures are journaled (R-RUN-9–12).
- The agent applies a configuration change the owner approved (for example
  a module's setting or a limit), refuses one that fails the config's
  checks, and rolls back on request.
- **Gate:** the Deye module runs on cron without the model, its versions
  journaled; the family bot has migrated and costs less than the current
  setup.

Testing uses a separate bot: two processes with the same token cannot
coexist. The family bot migrates at the end of P1; the current bot stays as a
fallback until the gate is passed.

## 11. Open decisions

| Question | Options | Leaning towards | Decided by |
|---|---|---|---|
| Language of model-written modules | TinyGo · Go · Rust · AssemblyScript | Decided (owner, 2026-11-09, [ADR-0063](adr/0063-module-language.md)): Rust by default, TinyGo for existing Go code, standard Go only when TinyGo cannot | — |
| Tuya access | Cloud · LAN | Cloud: the internet stays up during outages (0.7); LAN later with a home runner | With load shedding, after P1 (ADR-0058) |
| How the node itself is updated | Deploy on every push · an image per tag · a chat command through the platform's API · the node replaces its binary from a signed release | Decided (owner, 2026-11-13, [ADR-0081](adr/0081-update-the-node-from-signed-releases-at-the-owners-command.md)): the signed binary, at the owner's command `/update`, with a fall back to the previous version (R-UPD-1…7); the image's environment stays a manual rebuild | — |
| Worker protocol | NATS request/reply · gRPC · HTTP | NATS: one bus for events and tasks | Two-node spike in P2 |
| History and tool format | Own · Anthropic Messages · OpenAI | Decided: neutral own format + MCP tool schemas ([ADR-0020](adr/0020-neutral-history-as-conversation-state.md)) | — |
| First API provider | Anthropic · Gemini · OpenAI-compatible | Decided for development: the operator's choice ([ADR-0021](adr/0021-providers-chosen-by-the-operator-claude-cli-included.md)); the claude CLI only, as chosen in [ADR-0047](adr/0047-sonnet-answers-during-development.md); OpenRouter fallbacks are a later topic | Revisit with more users or stronger tasks |
| Choosing models by purpose | Tiers and skill frontmatter only · a router classifier (layer 4) · per-purpose profiles | Decided for roles (owner, 2026-11-09, [ADR-0060](adr/0060-models-by-purpose.md)): purposes `chat`, `check`, `compact`, `code`, each an ordered list with an optional effort (R-RTR-6). Embeddings and vision wait in "Not now"; the classifier stays P2 | — |
| Where nodes live | Cloud hosting · Pi · home PC | Edge in the cloud or on a Pi, claude CLI worker on the owner's PC | The P1 monthly bill |
| Sleep and wake-up | Always-on process · scale-to-zero | Always-on edge, workers may sleep | Same |
| Sandbox for scripts and MCP modules | Separate uid + Landlock · bubblewrap/nsjail · WASM | Closed for host commands and skill scripts by [ADR-0039](adr/0039-command-sandbox.md) (accepted 2026-11-08): `no_new_privs`, a clean environment, Landlock, no Unix sockets below Landlock 9, the node not dumpable, fail closed | Railway and a Pi measured with `tools/sandboxprobe`; MCP modules in P3 |
| How long ended sessions (episodes) are kept | Forever, searched by `memory.recall` (today: the newest 500 per chat) · a retention: sessions older than N days are dropped after consolidation has read them | A retention after consolidation (owner, 2026-11-03): nothing is dropped that consolidation has not read, so a fact the owner never confirmed is still turned into one or kept; what was said by chance fades | M7 task 7.2, with the nightly consolidation; N is chosen there |
| Telemetry of edge nodes | Each edge to an OpenTelemetry collector over OTLP · Prometheus scrapes each edge · snapshots over the NATS bus, collected by the core | Decided by the owner (2026-11-03): over the bus, collected by the core, without JetStream for metrics (R-OBS-9). A collector needs one more link and one more service for every edge; the bus is already there. An outside collector can later be fed by the core | ADR at the start of P2, with the protocol |
| Home Assistant | WebSocket API · HA MCP server · MQTT discovery | Deferred to P3 | HA in Docker for testing |
| Handing a secret from the owner to the node | Environment variables only · `ganglion secret set` on the host · a chat message the transport takes and deletes · a page that encrypts in the browser and returns the ciphertext through the chat · a form posted to the node | The encrypted store and the command on the host are decided and in M6 ([ADR-0077](adr/0077-encrypted-secret-store-on-the-nodes-key.md), R-CFG-9, R-CFG-10). Still open: the page served by the node with the ciphertext returned through Telegram first (owner's brainstorm, 2026-11-12, [design note](plans/secrets-intake-design.md)); its phase follows the spike | A spike on Telegram's `sendData`, then an ADR |
| Logging the claude CLI provider in | A variable set by hand from `claude setup-token` · a `/login` command of the core that drives the bundled CLI and takes the one-time code from the chat · the same command with the node doing the OAuth exchange itself | The `/login` command driving the CLI; the token goes to the encrypted store and never to the chat (owner's request, 2026-11-13, [design note](plans/model-login-from-chat-design.md)). The phase is open; the store itself is in M6 | A spike on driving `setup-token` without a terminal, then an ADR |
| How Telegram updates reach the node | Long polling only · a webhook only · both, chosen in `ganglion.yaml` · both, chosen by the node | Decided (owner, 2026-11-13, [ADR-0078](adr/0078-telegram-updates-by-polling-or-webhook.md)): both, chosen in the file, polling by default (R-DLV-18, R-DLV-19, R-CFG-11; [design note](plans/telegram-webhook-design.md)) | — |
| When the repository opens | Private until the public release in P3 · open now with no announcement, the announced release in P3 · a fresh repository without the history | Decided (owner, 2026-11-14, [ADR-0082](adr/0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md)): open now, 0.x with no guarantees, bug reports in GitHub Issues, vulnerabilities reported privately; the announced release stays in the P3 gate | — |

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
| Open-source burden | Issues eat development time | 0.x and "no guarantees" in the README; the repository is open with no announcement, for invited colleagues to try and report ([ADR-0082](adr/0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md)); the public release with an announcement no earlier than P3 |
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
- Changing a field of the node's file without a restart (owner, 2026-11-16,
  [ADR-0093](adr/0093-config-changes-through-the-agent-with-history-in-the-workspace.md)):
  each field needs its own reload path; taken up one field at a time when a
  restart gets in the way.
- The first install of a node (owner, 2026-11-13): a script that takes a
  machine with nothing on it to a running node. It fetches a release and
  checks its signature
  ([ADR-0081](adr/0081-update-the-node-from-signed-releases-at-the-owners-command.md)),
  checks what the machine offers (the sandbox's kernel features, the
  toolchain for builds), creates the node's user and its data folder,
  writes a first `ganglion.yaml`, and does not finish until one channel
  works: today that is Telegram, so it asks for the bot token and the
  admin, puts the token where the node reads it without leaving it in the
  shell's history, and waits for the admin's first message. It also sets
  up the supervisor that restarts the node. Larger than it looks, a step
  of its own with its own ADR: where the token goes (the secret store or a
  variable), which supervisors and platforms it knows (systemd, a
  container, Railway), and what it does on a machine that already has a
  node. It comes after ADR-0081: the releases it installs are made there.
- Tiny trained models inside modules (owner, 2026-11-11): the agent trains
  a model of tens to a few thousand parameters (a decision tree, a
  logistic regression, a small neural net) for one narrow task and ships
  it as a module whose inference is plain arithmetic in WASM, returning a
  number or a flag as 9.4 asks. Plain if-else stays the default; a model
  is for a rule nobody can write down but the data shows (time to an
  empty battery, an odd consumption pattern, the router classifier
  above). Open, each needing an ADR: where training runs (a skill
  script, online in the module with weights in its cells, a worker marked
  like `builder`), the format of the weights, and how the owner vouches
  for weights nobody can read, trained on outside data
  ([ADR-0067](adr/0067-agent-writes-module-source.md),
  [ADR-0068](adr/0068-vouch-for-module-source-per-file-and-vetted-crates.md)
  vouch for source only).
- Memory (M5 design, 2026-11-11): a model call after every turn to pick
  out facts (rejected for its cost, ADR-0072); storing what the agent
  found out itself before consolidation looks at it; finding a folded
  topic by keywords or embeddings (R-MEM-4).
- A global kill switch for every tool.
- Docker from commands below Landlock ABI 9.
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
  The public listener itself left this list in a narrow form, for the
  Telegram webhook only (R-CFG-11,
  [ADR-0078](adr/0078-telegram-updates-by-polling-or-webhook.md)); pages
  and `initData` stay here.
- Telegram widgets (Bot API 10.4, owner, 2026-11-16,
  [research](research/2026-11-16-telegram-widgets.md)): a small HTML
  widget inside a bot's message, sandboxed with no network, whose state
  only the bot changes, silently. It needs no HTTP server and no
  `initData`, so it covers part of what Mini Apps would give. Uses seen:
  a live panel of a module's cells updated with no model call, the card
  of a background job (R-LIVE-9), a turn's plan as a checklist, `/status`
  as a live summary, charts. Production allows private chats only, and a
  widget cannot be forwarded or copied, so a plain or rich message stays
  the fallback. Open, needing an ADR: who writes a widget's HTML
  (templates in the transport, a module's own vouched widget, or the
  model), and how a `widget_query`, answered within 15 s, goes through
  the inbox. New Telegram features are to be weighed for the bot as they
  come (owner).
- Packaging and install paths for other users beyond the image published
  by tag (R-UPD-8, [ADR-0086](adr/0086-publish-the-nodes-image-to-ghcr-on-a-core-tag.md)):
  a Railway template, a Helm chart, a systemd unit for a Pi,
  `ganglion init` (closer to the public release in P3).
- First-run onboarding without hand-written ids: the node prints a one-time
  pairing code, and the first person to send it to the bot becomes the
  admin (the same idea as node enrolment with a one-time token, 9.3).
- Handing a secret to the node from the chat (the store itself is in M6,
  [ADR-0077](adr/0077-encrypted-secret-store-on-the-nodes-key.md)): a
  page that encrypts the value in the browser, so it passes neither the
  agent nor the chat's history; a build never gets a secret; a device gets
  its Wi-Fi credentials when it is provisioned, not in its firmware
  (owner's brainstorm, 2026-11-12,
  [design note](plans/secrets-intake-design.md); section 11).
- Logging the model provider in from the chat: the owner sends `/login`,
  the node answers it without a model, sends the sign-in link and takes the
  one-time code before the inbox; the token goes to the secret store. With
  a Railway template the install then needs no shell in the container
  (owner's request, 2026-11-13,
  [design note](plans/model-login-from-chat-design.md); section 11).
- Telegram, beyond formatting, typing and replies (ADR-0025). Each item
  is to be checked against the Bot API docs of the day:
  - a reaction in groups, useful when someone addresses the agent directly;
    groups have their own allowed reactions and everyone sees one (private chats:
    R-LIVE-7);
  - streaming answers as they are generated (needs streaming providers
    and must fit the outbox's delivery guarantee);
  - per-group behaviour and limits in the node config, beyond connecting a group (R-DLV-17).
- Voice messages: understanding them, later, as a separate module or
  model (owner, 2026-11-02).
- Around live turns (2026-11-05):
  - `/stop` and interrupting a run. The owner's case is adding, not stopping, and what
    a tool already did cannot be undone.
  - A reaction chosen by the model, "if it is really needed" (owner).
  - A quiet window before a turn starts: rejected by the owner for now, since additions
    come well after the first message.
  - Status on each line of a plan by reactions: Telegram puts a reaction on a whole
    message.
  - Parallel tool calls in the API route. A plan's lines must not stand in the way.
  - The text a CLI run wrote before it was ended, which needs `stream-json` on the main
    route (ADR-0050).
  - Keeping the history in the prompt cache across CLI runs. Each run writes the whole
    transcript to the cache again, as [ADR-0021](adr/0021-providers-chosen-by-the-operator-claude-cli-included.md)
    accepted; `--resume` would keep it, at the price of a second copy of the history.
    Worth another look with real turns' numbers
    ([measurement](research/2026-11-05-cost-of-an-addition.md)).
  - Compacting a session after a long silence, as the owner saw Claude do: the cache
    has gone cold by then, so it costs no cache, and the next call is smaller.
  - The agent making a file and sending it, such as a report as a PDF (owner's live try,
    2026-11-05): the transport can send documents, but no tool makes one. It said it had
    none and offered text to copy.
    The owner wants this for certain (2026-11-06): archive a project folder and send it, find photos by
    age and send them. Taking files from the machine depends on M3d's sandboxed commands, and what may
    leave the node needs a security pass.
  - Where the person lives, as a setting of the node: a region for the search backend, and
    hosts it never offers. The owner's live try got Russian sites that are blocked for them,
    since the request was in Russian; a fact in memory is the first thing to try (2026-11-05).
  - A sign of life in the plan's message: when a step takes long, OpenClaw writes "working…" there,
    and the owner noticed it (2026-11-06). Reactions and typing already show long work
    ([ADR-0051](adr/0051-status-reactions-outside-the-outbox.md)); whether a word in the plan adds to them
    is to be seen on a live run. It must go once the work moves on, since the chat stays clean (R-POL-9).
    After a plan says it finished, the owner waited about 10 s for the long report with nothing to show for it
    (2026-11-06): a message such as "…" that the answer then replaces would fill that gap. `editMessageText`
    takes a rich message, but the first form and its edit are for a design to settle.
  - A forward with a comment: Telegram delivers the comment first and the forward right after, in the same
    second. The turn starts on the comment alone and answers that it sees no forward; the forward then joins, the first
    answer folds (R-LIVE-1) and the second is right. Nothing is lost, but it costs a model call and a useless folded
    answer (seen live, 2026-11-06). To check first: whether both arrive in one `getUpdates` batch, since
    taking the whole batch before the first step would then fix it with no waiting.
  - URL previews: the Telegram transport does not set them, so Telegram's default applies on ordinary
    messages, and `sendRichMessage` has no parameter for them. A URL the model writes may be fetched by
    Telegram with no press (found 2026-11-06, [ADR-0054](adr/0054-structured-replies-as-rich-blocks-the-transport-builds.md)).
  - Media inside a rich message, streaming through `sendRichMessageDraft`, buttons inside a message, and
    long replies with no structure as one rich message (ADR-0054).
  - A classifier that tells a question from a task, as a candidate first client of "Models
    by purpose": only if the main model's own judgement, given a clearer rule, is measured
    and falls short (owner, 2026-11-05).
  - Fewer presses for pages that differ in one parameter: on the owner's live run of 2026-11-07,
    "the exchange rates" asked twice (one URL for the dollar, one for the euro), and a reply
    "and in the west?" to a weather answer asked again for the same site with another city. The
    owner's idea: a quick model judges whether a new URL only changes a parameter of one already
    approved, and the node asks only when it says no or no such model is configured. The security
    side is decided first (owner): R-WEB-2 asks because a query string the model built is a
    channel out, so "only a parameter changed" is exactly what an exfiltration looks like, and a
    model's verdict on it would be a decision made by a model under outside influence. Options to
    weigh in its own brainstorm: one press for a plan's listed URLs (as R-CMD-7 does for
    commands), an approval for a host and path with the parameter values shown, or the model's
    verdict only in a clean session (R-POL-6).
- `notify` beyond what R-RUN-18 gives: a module sending different
  messages to different places (a kind on the message that the node's file
  routes by), and the routing on an event bus, where `notify` is a publish
  and `notify_to` a subscription (owner, 2026-11-13,
  [ADR-0079](adr/0079-a-modules-messages-go-where-the-nodes-file-says.md)).
- A module asking a model, the cheapest one, to word a message or for a
  light analysis: the module would write a prompt and spend the owner's
  limits, so it is its own security boundary, designed when the first
  module needs it. The ideal module needs no model (owner, 2026-11-10).
- Deleting a module's source file with a tool (`module.write` only writes;
  M4c).
- Hooks and pipelines: an internal n8n (owner, 2026-11-03). M4c takes the first
  step: reflex modules with a `notify` capability; the rest stays here.
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
- PDF generation: deferred, since Cyrillic fonts add weight; replies
  carry pictures and documents as attachments first (R-DLV-9) (owner,
  2026-11-02).
- Installing a node with one link or one button, perhaps from a site, for a person who is
  not a developer: they give their Telegram id or a channel, then the first-run introduction
  takes over. So the node's admin cannot be assumed to be technical (owner, 2026-11-06).
- Importers from popular assistants (OpenClaw first): persona, memory,
  skills and settings turned into ganglion's workspace files, so their
  users can move over (R-MEM-3, ADR-0024). The OpenClaw importer must also
  bring its MCP servers: the owner's daily Jira report runs on one (owner,
  2026-11-06). This needs ganglion as an MCP client, which it is not yet, and
  a server's secrets are not copied silently.
- Audiences and several agents: a "family mode" with its own agent or
  settings for a group of people, per-topic setups in Telegram forums,
  sub-agents and parallel agents. Until there is a public repository
  with install docs and settled processes, a node is one operator, one
  agent, one bot (owner, 2026-11-02).
  - Who uses a node is not settled, and "family" is only the owner's own case: a home, a company
    chat or any other group may use one, paying through an API such as OpenRouter. To work out,
    after research: limiting Telegram users (groups are R-DLV-17), which tools a person or a group gets and
    how that is managed, and whether an agent becomes an abstraction, say an admin agent in a private
    chat and another for a group, with its own tools and security. The role name `family` in the code
    means "allowed and not an admin" and should not shape this (owner, 2026-11-06).
  - Settings as a hierarchy (owner, 2026-11-06): a language for a chat or a group, and within it a
    forum and a forum's topic, set in the configuration; and what a person may do, such as choosing
    their own language, as a group of permissions named in the configuration.
  - Roles a group's line may name beyond `family`, each with its own tools and filters, so that what
    a group may do is set per group (owner, 2026-11-13). The `role` field of a line of
    `telegram.groups` is where they plug in; until then a group gets conversation with no tools.
  - Where a bot answers: the owner's ruling of 2026-11-06 is now R-DLV-16 and R-DLV-17. What stays for
    later, as a plugin or a worker: reading everything and working out what to react to, such as in
    an automated chat, needs a classifier and its own targeting.
- Models by purpose (owner, 2026-11-05): a role for short decisions, embedding
  models for memory and search, models that read pictures. Whoever sets up an
  agent picks its models, and may have only an OpenAI-compatible API, so ganglion
  does not rank them against one model. Each is taken up when a concrete task
  works badly without it:
  - **A role for short decisions:** done in M4 as the `check` purpose
    (R-RTR-6); a model listed only under `check` never lands in the `chat`
    list for a person's message. A model check of commands can only add to M3d's class
    table, never replace it, because a model can be injected.
  - **Embeddings:** when memory search starts to miss (today a substring scan of
    the newest 500 sessions), with a research note first.
  - **`guardtest`'s case table** may become a command with which an operator
    checks the model they chose for the memory check, a security boundary.
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
- The agent creates and manages skills itself: it drafts one from real work,
  a clean-context model checks it, and the owner accepts it; the owner still
  sees what will run. This changes "only the owner installs a skill"
  (R-RTR-4, ADR-0042). Its own brainstorm with a research note on current
  practice (the Agent Skills format, neighbours' learning loops, malicious
  skill campaigns). Markdown skills could come first; scripts need the M3d
  sandbox (owner, 2026-11-04).
- Fallback models on OpenRouter: while the base mechanisms are built,
  development runs on the main model only; fallbacks are a separate topic
  later (owner, 2026-11-04).
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
- `ganglion doctor`: a terminal command that checks a node's settings for
  likely mistakes and misconfigurations, beside `ganglion mcp`; not in the
  chat, where `/status` assumes the node runs (owner, 2026-11-08).
- Choosing the model in the chat from the catalogue's models, per session;
  `/status` already shows the session's model, so it needs no change then
  (owner, 2026-11-08).
- Raised privileges for managing remote servers: commands that need root,
  given by the owner from the chat in a way that stays safe in transit and
  on the node. Today `sudo` and its kin are forbidden (R-CMD-2), and the
  owner runs such a command by hand from a command the agent prepared, as
  desktop agents do. How to authorise and protect such a command is open;
  revisit after the base mechanisms are built, checked and the repository
  is public (owner, 2026-11-08).
- Host commands on Windows (and macOS): today the command sandbox is
  Linux only and the tool is off elsewhere (R-CMD-3, ADR-0039). Windows
  needs its own mechanism (AppContainer, restricted tokens, job objects),
  with its own ADR; WSL2 runs a Linux kernel, so the probe there
  (`tools/sandboxprobe`) says whether the Linux sandbox already holds.
  To come back to, not to drop (owner, 2026-11-08).
