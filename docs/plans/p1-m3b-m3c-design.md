# M3b and M3c design: the agent works with the world

Design from the owner's brainstorm of 2026-11-02. It is the input for the
spec changes, the ADRs and the two step-by-step plans
(`p1-m3b-*.md`, `p1-m3c-*.md`). It is not itself a plan.

Marks: **owner** means the owner decided it in the brainstorm; **call**
means the author's default, open to the owner's correction.

## 1. Intent

After M3a the bot talks; it does not yet act. The owner's test for "this is
an agent, not a chatbot" is a task that takes several linked steps:
remember things, use tools, run on a schedule, read the web, and show the
result. Two concrete checks (owner):

- **Currency rates.** "Every hour, until I cancel, send me the exchange
  rates" (hourly while testing, daily afterwards). Each run compares with
  the previous runs. Cancelled in words or by a button.
- **Agent metrics as a picture.** "Show me the agent's metrics" → an image
  in Telegram (M3c, through a promtop skill).

The Deye battery check is the same pattern but needs the Deye module wired
into the runner; it is the acceptance of M4, not of M3b.

Security is a design input, not a review step (owner): every part states
its trust boundary and abuse cases; default deny; any widening of rights
needs the owner's explicit approval; nothing is replaced silently under the
same name. A self-hosted node can be changed by its owner; a node in a
cloud must not be changeable from outside.

## 2. Goals, non-goals, anti-goals

**Goals**

- Tools with policies and a security baseline (section 4).
- Long-term memory the agent manages itself, safe against poisoning.
- A scheduler with reminders and scheduled agent tasks, listed with quick
  actions on buttons.
- Reading the web: `web.search`, `web.fetch`, HTML turned into Markdown by
  a WASM module.
- Modules shipped and updated independently of the core (M3c: update and
  rollback).
- Skills, level 1 and 2 (M3c).
- Diagnosis by a model when something goes wrong: a tool-call journal,
  logs on disk, and a read-only MCP entry point (M3c).

**Non-goals (later phases, owner)**

- The agent writing and building its own modules: P3 (skill compilation),
  through a worker that does not exist yet (P2).
- The agent changing its own configuration: M6 (`config.propose`). In M3b
  the agent may find a cause and propose a fix; the owner applies it.
- Voice messages: later, as a separate module or model; goes to "Not now".
- External MCP servers as a client (GitHub, browser, Home Assistant): P3.
- PDF generation: deferred (Cyrillic fonts add weight); attachments first.
- Buttons declared by modules in their manifests: still P3 (spec 14). M3b
  buttons belong to the core.
- Embedding search over memory: P2 (R-MEM-4).

**Anti-goals**

- A tool or text from outside that can change the agent's policies,
  directives, persona or skills.
- A replacement of a built-in module or skill under the same name by
  anyone other than the project's release channel.
- An open network port for diagnostics.
- Logging that loses the last lines before a crash, or fills the disk.
- Growing the phase without recording it: everything here is added to the
  spec first (section 9).

## 3. Constraints

- One operator, one agent, one bot (spec 14).
- Core and edge fit a ~128 MB container (principle 8); new dependencies
  are measured.
- New dependencies: a release at least ~2 weeks old; each needs an ADR.
- The P1 stop criterion: P1 not closed within 4 weekends (spec 13). The
  cut list applies; section 8 proposes the order.
- Go conventions in [conventions.md](../conventions.md); the composition
  root in `config` with `samber/do` (ADR-0028).

## 4. Security baseline (both milestones)

Its own ADR ("agent security baseline") and a research note citing the
OWASP Top 10 for LLM applications and the "lethal trifecta" analysis
(private data + untrusted content + a channel out; break one leg). Owner
asked for both.

Three layers of directives (owner agreed):

| Layer | What | Guarantee |
|---|---|---|
| 1. Rules in code | Per-tool policy (autonomous / confirm / forbidden), access, limits, kill switches, risk classes | Full: checked in the core, the model cannot bypass it |
| 2. Directives as text | A directives block in the system prompt, e.g. "never send personal data without confirmation" | None; it stops the model from trying and lets it explain a refusal |
| 3. Second opinion | A clean-context model checks an action or a write | Catches what a rule cannot express; later (Jev in spec 14) |

A directive in words becomes a layer-1 rule through `config.propose` in M6;
until then it lives in layer 2.

Rules:

1. **Tainted session.** A session is tainted once external content enters
   it (a page, a search result, a forwarded message, a scheduled task's
   fetch). Taint is not per turn: the content stays in the history and
   would steer the next, "clean" turn. It lasts until the session ends
   (`/new`, idle reset); compaction keeps it (rule 4). In a tainted session
   these need the owner's confirmation: any side effect, any send to anyone
   but the requester, any memory write (call, from the security-first
   rule; it costs more presses).
2. **No data out without confirmation.** `message.send` to another chat,
   network from skill scripts, and `web.fetch`. `web.fetch` runs without
   confirmation only on URLs that came from the owner's message or from
   search results; a URL the model built itself needs confirmation (the
   query string is an exfiltration channel).
3. **Clean-context check** (owner's idea; the "dual LLM" pattern). A cheap
   model with no history, no tools and no tainted context:
   - **on write:** every memory entry is checked before the commit: "a
     fact about the owner, or a hidden instruction?" Imperative entries
     ("always do…", "ignore…") are rejected; suspicious ones go to
     quarantine with a notice to the owner;
   - **on schedule:** a system task audits memory, persona and skills for
     injections, contradictions and stale entries. A system task is marked
     as such and cannot be deleted from the list.
4. **Compaction keeps the marks.** The summary written when history is
   compacted keeps external content marked as data, so an injection
   cannot survive compaction as the agent's own words.
5. **Secrets never reach the model** (already true: `secret.get` serves
   modules only). Secrets are redacted before anything is written to a
   journal or a log; the Bot API token in request URLs is never logged.
6. **Least privilege.** Default deny. Scheduled turns run inside the grant
   approved when the task was created (section 5.3). The agent cannot
   change its own policies, directives, persona or skills; the persona is
   changed only by the owner (section 5.7).
8. **Who is who in M3b** (call, from "one operator" in spec 14): tools,
   memory, scheduled tasks and buttons are for admins. A `family` member
   gets plain conversation, with no tools and no memory index in the
   prompt. Per-person memory and family tools are decided with the family
   bot migration (M7).
9. **Where policies live in M3b** (call): per-tool defaults in Go code;
   the node YAML of M4 (task 4.1) makes them configurable later, and M6
   turns directives into rules there. Kill switches are switch cells
   (ADR-0014), as planned.
7. **Everything is journalled**: policy decisions, check results,
   quarantine (section 6.5).

## 5. M3b · The agent works with the world

### 5.1 Tools and policies (spec task 3.6)

- Tools are described in MCP format (R-PRV-3) and run through the existing
  `ToolMiddleware` chain in `agent`: policy → taint → journal → call.
- Policy per tool: `autonomous`, `confirm`, `forbidden`; side-effecting
  tools default to `confirm` (R-POL-4).
- Side effects get the idempotency key `turn_id + call number` (R-DLV-6).
- The depth limit on self-calling chains (R-AGT-4) and the tool placement
  interface (R-RTR-3) land here (carried from M3a).
- Base tools: `schedule.*`, `message.send`, `cells.get` / `cells.set`
  (cells stay on the `virtual` backend), `memory.*`, `web.search`,
  `web.fetch`.

### 5.2 Confirmations and quick actions on buttons

- Telegram inline keyboards and `callback_query` in the transport; the
  transport stays the only place that knows Telegram (ADR-0017, ADR-0018).
- A confirmation is a request cell (ADR-0014): "Run / Cancel"; no answer
  within `ttl` denies (R-POL-2).
- A press does not go through the model: it is a direct tool call with
  arguments fixed when the button was made, through the same policies.
  After a press the message is edited in place ("deleted", "done").
- **A confirmation asked in the middle of a turn resumes the conversation**
  (call). A turn cannot wait for a press: the turn timeout is minutes,
  `ttl` can be longer, and the press would queue behind the waiting turn
  (one turn at a time per conversation). So the turn ends with the call
  pending and a tool result "awaiting confirmation"; the press (or the
  expiry) is a new event in that conversation, which runs the stored call
  and starts a turn with its real result, so the model continues (e.g. it
  reads the page it asked to fetch). Quick actions on list entries
  (Delete, Details) do not resume a turn; they edit the message.
- **Abuse cases:** the button carries only a random one-time id (the
  action is stored in the database; Telegram's 64-byte `callback_data`
  fits it); only the addressee with access may press; the button expires;
  a double press does not run twice; "Run now" on a task with side effects
  still asks for confirmation.

### 5.3 Scheduler, reminders and scheduled agent tasks

- The scheduler moves up from M4 (task 4.2): cron in Europe/Kyiv,
  `cron_runs`, catch-up runs each missed job once, `idemKey` = job +
  scheduled time. M4 reuses it for modules.
- Two kinds of jobs (owner): a **reminder** (a fixed text at a time) and a
  **task** (an instruction run as a full agent turn with tools).
- Created in words ("every hour send me the rates"); creating a recurring
  task asks for confirmation, since it runs unattended and spends tokens.
- Listed in words ("show my reminders"): the agent calls `schedule.list`
  and replies with one entry per item and buttons: Details, Delete, and
  Run now for tasks (owner).
- **Each task has its own session** (call): its earlier runs are its
  history, so "compare with the previous rates" needs no memory tool;
  compaction keeps it small.
- **A task carries a grant** (call). The creation confirmation shows what
  the runs may do: the instruction, the schedule, the hosts `web.fetch` may
  reach (e.g. `bank.gov.ua`), the tools, and who receives the reply. The
  owner approves it once; the runs then need no presses. A run that needs
  anything outside its grant is denied, and the owner gets one notice with
  the reason; there is no button on every run.
- A task runs as its creator, with the creator's rights; its instruction
  and grant are fixed at creation, so a fetched page cannot rewrite them.
  Scheduled turns count towards the daily limit.

### 5.4 Memory (spec task 3.5)

Four levels as in spec 7.7; M3b builds facts and episodic search.

| Level | M3b |
|---|---|
| Working | history and compaction (M3a) |
| Facts | `memory/*.md` in the workspace git repo; an index in the system prompt; bodies on demand |
| Episodes | search over past turns in SQLite (full text); embeddings P2 |
| Procedures | skills (M3c) |

- **The agent decides what to remember** (owner); "remember this" is just
  one case of it.
- Memory holds facts, not instructions. Facts are shown to the model as a
  "known facts" data block. Procedures go to skills (the agent proposes, the
  owner installs); directives and policies go to config and code.
- An owner's preference from an untainted turn is stored as a fact about
  the owner ("the owner does not want messages 23:00–07:00").
- Every entry records where it came from: turn id, tainted or not, date.
- Writes from tainted turns need confirmation; every write passes the
  clean-context check; every change is a git commit, so a bad entry is
  reverted.
- "What do you remember about me?" lists it.
- The workspace becomes a git repository (ADR-0024 planned this). Decision
  D-7: the `git` binary (recommended) or go-git → ADR.
- Nightly consolidation (R-MEM-1) is not in M3b; the scheduled audit of
  rule 3 is.

### 5.5 Reading the web

- `web.fetch` in the core: http(s) only, no private or loopback addresses
  (re-checked after redirects), a size and time limit, the URL rule of
  section 4 rule 2. It passes the HTML to the `html2md` module and returns
  Markdown marked as external data, truncated to a limit. JSON and plain
  text pass through without conversion (the rates come from a JSON API,
  e.g. the National Bank of Ukraine's, with no key).
- `html2md` is a WASM module (owner): main-content extraction in the
  manner of Readability, then Markdown (headings, lists, links, tables).
  It is a pure function with no host functions granted: no network, no
  state. Hostile HTML is parsed in the sandbox, not in the core.
- **Why a module and not a library (owner):** its parser library is
  updated by shipping one `.wasm`, without a new core release.
- Language (Rust or TinyGo) is chosen after a short spike on size and the
  quality of available parsers (call).
- In M3b the module is embedded in the binary (`go:embed`); updates come
  in M3c and are stored on the data volume, taking precedence over the
  embedded version.
- This pulls a minimal "module as a tool" path forward from M4 (task 4.4);
  M4 needs it anyway for `deye.status`.
- `web.search`: one backend behind an interface. Default is a key-free
  backend; keyed ones (Brave, Tavily, Exa) are enabled by setting a key.
  OpenClaw ships DuckDuckGo (an unofficial HTML scrape, "expect breakage")
  and SearXNG (self-hosted) as its key-free options. Which key-free option
  ganglion uses is decided in the ADR (call: compare reliability; a scrape
  that breaks is a poor default).

### 5.6 Tool-call journal

- A `tool_calls` table beside `turns`: turn id, call number, tool,
  arguments (secrets redacted, size-capped), result size and head, error,
  duration, policy decision (allowed, confirmed, denied, expired), taint.
  It is also the audit log (R-POL-3).
- Retention by age.
- Built for diagnosis by a model (owner's use case): one turn id ties the
  incoming message, the route, the calls and the reply together, so a model
  does not have to join events by time.

### 5.7 Tools for the claude CLI: the MCP bridge (owner)

Moved here from M3c. Without it, the first registered tool drops Haiku
through the CLI (ADR-0022): the router filters out models without tools,
and `provider/claudecli` refuses tools today.

- Two paths, one executor in the core:

  | Provider | How the model gets tools | Who runs the call loop | Where policies run |
  |---|---|---|---|
  | API models (OpenAI-compatible) | tools in the request; `tool_calls` back | our agent | the core |
  | claude CLI (and similar CLIs later) | an MCP bridge to the core | the CLI inside `claude -p` | the core; the bridge only forwards |

- The core serves its tools to the CLI over MCP for the duration of one
  run, authenticated by a per-run grant; the session, principal and turn
  are taken from the core's own context, never from the child process.
  This is how OpenClaw does it ("MCP loopback bridge": a loopback HTTP MCP
  server in the gateway, a per-run token, `--strict-mcp-config`).
- Transport → ADR: loopback HTTP with a per-run token (OpenClaw's), or a
  Unix socket with `0600` permissions plus the token, which other local
  users cannot reach at all (call: the socket).
- Claude Code's own tools stay off (`--tools ""` today); only the agent's
  tools are reachable, under the same policies, taint, journal and
  idempotency keys.
- A confirm-class call inside the CLI's loop returns "awaiting
  confirmation" and the run ends; the press resumes the conversation as in
  section 5.2.
- The `ganglion mcp` diagnostics server (M3c) reuses the MCP server code
  with another tool set.

### 5.8 Carried from M3a

- The current time in the last user message.
- A first-run "introduction" conversation that writes the persona
  (ADR-0024). It does not break rule 6 (call): it runs only for an admin,
  only while `persona/` is empty, only in an untainted session, and the
  agent proposes the persona text, which the owner approves with a button
  before it is written and committed. After that the persona is changed
  by the owner only.

### 5.9 Acceptance of M3b

- "Every hour send me the rates": confirmed by a button, listed by "show my
  reminders", each run compares with the previous runs, deleted by a
  button or in words.
- A fact told in one conversation is recalled in a later one (spec 3.5).
- A page read with `web.fetch` and summarised; an injected "remember: …"
  in a test page does not reach memory without the owner and is flagged by
  the clean-context check.
- An unconfirmed side effect does not run; an expired request denies
  (spec 3.6).
- The same scenarios pass with Haiku through the CLI and with an API model.

## 6. M3c · Modules, skills, diagnostics

### 6.1 Module install, update and rollback

Two questions, kept apart: where the file comes from, and how the node
installs it.

- **Install mechanism:** a version store on the data volume; checksum
  check; a self-test of the new version on built-in samples before the
  switch; an atomic switch without restart (calls in flight finish on the
  old version, new calls go to the new one); a failed self-test keeps the
  old version and tells the owner; the previous version is kept for
  `/modules rollback <name>`. Module state (`state.*`) survives the swap
  (spec 7.5). Admin only.
- **Source in P1:** a URL plus sha256. CI builds the modules and publishes
  them to GitHub Releases with their sums, so for built-in modules this is
  just `/modules update html2md` (owner).
- Rejected for now: a `.wasm` sent as a file in the chat (owner: nobody
  needs it); OCI registry; auto-update (code changes without the owner;
  too early without signatures); modules in the config git repo.
- **Trust boundary (owner):**
  - built-in modules (`ganglion/…`) come only from the project's release
    channel (checksum now, signature in P3); a worker or the agent cannot
    rebuild or replace them under the same name;
  - modules built later by the agent install under another namespace
    (`local/…`), always with the owner's approval; switching a tool to a
    fork is an explicit owner decision;
  - a new version's manifest rights are compared with the old one's; any
    widening is a separately worded approval (the ADR-0014 principle).
  - **"New rights only with a project release"** (call, the strict
    reading): the core binary embeds, for each built-in module, the ABI
    version and a ceiling of rights. A release whose manifest asks for more
    is refused until the core itself is upgraded.
  - Module releases are tagged on their own (`html2md/v0.3.0`), separate
    from core releases; `/modules update` takes the newest release whose
    ABI version this core supports. CI gains a release workflow for
    modules (none exists today).
- Why the node never compiles modules: a toolchain is hundreds of MB to
  over a GB (principle 8), slow on a Pi, fetches dependencies at install
  time (supply chain), and `.wasm` is portable anyway. Building belongs to
  a worker (P2–P3); the node only installs artifacts.
- Later: P2 distributes modules to edges over the NATS Object Store; P3
  adds signed one-click install. Both build on this mechanism.

### 6.2 Skills, level 1 and 2 (spec task 3.7)

- `workspace/skills/<name>/SKILL.md` with frontmatter (model requirements,
  read by the router); a level-2 skill may carry a script.
- Scripts run as subprocesses with an environment allowlist and a timeout
  (decision D-5 → ADR); network from scripts follows section 4 rule 2.
- Built-in skills are embedded in the binary, read-only; a workspace skill
  with the same name cannot silently replace one.
- The agent may propose a skill for a recurring procedure; the owner
  installs it.
- **promtop skill:** `promtop render` on the node's own `/metrics`: PNG
  for the owner, `-format summary` (JSON for agents) for the model.

### 6.3 Attachments

- `sendPhoto` and `sendDocument` in the transport; channel-neutral
  attachments in `Outgoing`, carried through the outbox with its delivery
  guarantee.
- First use: the promtop picture.

### 6.4 Logs on disk

- JSON lines in `<data>/logs/` beside stdout (stdout stays for Railway,
  journald and docker).
- Synchronous writes, so the last lines before a `kill -9` survive.
- Rotation by size, with limits on file count and age, so the disk never
  fills.
- Secrets redacted (section 4 rule 5).
- Library or a small own rotator → ADR (call: `lumberjack`, or own if the
  conventions favour no dependency). The owner's `go-app-tui-template`
  logging was reviewed and is not reused as is: it rotates with
  `lestrrat-go/file-rotatelogs` (archived since 2021, time-only, no size
  or count limit) and writes asynchronously through a bus (loses the queue
  on `kill -9`). Its configuration shape (sink, format, rotation) is a
  good model.

### 6.5 `ganglion mcp`: diagnostics from outside

The owner's case: when something is wrong, they ask the agent or an
external model (Claude Code) to read the logs, find the cause and fix the
configuration. If the agent is down, it cannot diagnose itself, so the
diagnostic path must not need a model or a running node.

- `ganglion mcp` is a subcommand: an MCP server over stdio. It opens the
  database read-only (WAL allows reading beside the running core; the
  ADR-0019 lock is for the writer only) and the log files, and offers
  read-only tools: search logs, search the tool-call journal, recent
  failed turns, the effective configuration with secrets redacted, queue
  state.
- Reached by running the command:
  - locally: `ganglion mcp --data <dir>` in Claude Code's MCP config;
  - another machine: `ssh <host> ganglion mcp --data …`;
  - containers: `railway ssh`, `docker exec -i`, `kubectl exec`.
- No open port; authentication is the platform's. Read access to the data
  directory is read access to all conversation history: the docs say so.
- To check in a spike: that `railway ssh` passes stdin and stdout through
  cleanly enough for MCP. Fallback: MCP over HTTP with an admin token,
  which is an exposed surface and needs its own ADR.
- No container image or Railway deploy is pulled forward (call): "in a
  container" in M3c means a local `docker exec`; the `railway ssh` check
  moves to the deploy task of M4 (4.6).
- The agent gets the same search tools (admin only, read-only, results
  marked as external data: logs contain fragments of fetched pages).
- MCP SDK: the official Go SDK or own minimal server → ADR (call).

### 6.6 Acceptance of M3c

- "Show me the agent's metrics" → a picture in Telegram.
- `/modules update html2md` installs a new release without restart; a
  broken release fails its self-test and stays off; rollback restores.
- A skill with a script runs; a variable outside the allowlist is not
  passed (spec 3.7).
- Claude Code diagnoses a seeded fault through `ganglion mcp`, locally and
  in a container.
- 3.8: end to end on the test bot under the M2 crash harness (spec 10.2).

## 7. Where each part lives (call)

| Part | Package (proposed) |
|---|---|
| Policies, taint, clean-context check | `policy` |
| Memory | `memory` |
| Scheduler | `schedule` |
| Web tools | `web` (fetch, search backends) |
| Tool-call journal | `turns` (beside the trace) or `journal` |
| Buttons and callbacks | `transport/telegram` + channel-neutral types in `event` / `transport` |
| Module install | `runner` (store, switch, self-test) |
| `html2md` | `modules/html2md` |
| Skills | `skills` |
| Diagnostics server | `cmd` (`mcp` command) + a package for the tools |

The builder may adjust names; the code map is updated in the same commit.

## 8. Size and order

M3b is over one weekend (the MCP bridge moved into it), M3c about one. Against the 4-weekend P1 budget
this is tight. Proposed cut order if the budget runs out (call): PDF
(already out), `web.search` reduced to keyed backends only, the scheduled
memory audit (keep the write-time check), `ganglion mcp` over `railway ssh`
(keep the local form).

## 9. Spec and documents to change first

- Spec: add web reading (`web.fetch`, `web.search`, `html2md`), module
  update and rollback, the tool-call journal, logs on disk, `ganglion mcp`,
  attachments, the security baseline; move the scheduler from M4 to M3b;
  "Not now" gains voice and PDF.
- Phase plan: split M3 into M3b and M3c with these tasks; M4's 4.2 and
  part of 4.4 move up.
- ADRs (Proposed, accepted by the owner):
  1. agent security baseline (+ research note);
  2. memory in a git workspace, D-7;
  3. web reading: `html2md` as a WASM module, search backends;
  4. module install, update and trust boundary;
  5. buttons, quick actions and resuming a turn after a confirmation;
  6. the MCP bridge for CLI providers;
  7. script sandbox, D-5 (M3c);
  8. logs on disk (M3c);
  9. `ganglion mcp` diagnostics (M3c).
- Architecture views: containers and components for policies, memory,
  scheduler, web and diagnostics.

## 10. Left to the builder

- Package names and internal APIs within the conventions.
- The exact journal and memory schemas.
- Truncation limits, retention periods, self-test samples.
- Test strategy per task, in the plans.
