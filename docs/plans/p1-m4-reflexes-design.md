# M4 design: node config, modules built by the agent, cells as registers

Design from the owner's brainstorm of 2026-11-09. It is the input for the
spec changes, the ADRs and the step-by-step plan (`p1-m4-*.md`). It is
not itself a plan.

Marks: **owner** means the owner decided it in the brainstorm; **call**
means the author's default, open to the owner's correction.

## 1. Intent

M4 was "reflexes: runner and Deye": the `deye-reader` module from P0 runs
inside the node on a schedule, writes the battery's state of charge and
the grid status into sensor cells, and the agent answers "how is the
battery?" through a tool. It was also meant to feed M5, the load-shedding
flow, and to start the "Deye two weeks without the model" clock of the P1
gate.

The brainstorm changed the frame in four ways:

- **Deye is not part of the agent** (owner). The agent ships with its own
  base modules (`html2md`). A module like `deye-reader` belongs to one
  owner's system: the agent builds it from source and installs it with
  the owner's approval. The node never accepts a ready-made `.wasm` from
  outside. In M4 the source comes from an example in the repository; in
  P3 the agent writes it.
- **Load shedding leaves P1** (owner). Switching loads by rules over
  sensor values is a flow engine: ready modules linked through cells,
  edited visually (in the spirit of Node-RED), by a person or by the
  model. That is far spec, next to 9.6.
- **No two-week clock** (owner). A module is built and started; from then
  on it is code like any other: bugs and exceptions are handled, new
  versions ship. What matters is the start point and a record of what was
  improved and why, to read later.
- **One config file** (owner). Everything an operator chooses lives in
  `ganglion.yaml`, models included; the environment keeps only what is
  needed to start and the secrets.

## 2. Goals, non-goals, anti-goals

Goals:

- `ganglion.yaml` is the node's single source of configuration, with a
  JSON Schema that documents every field.
- Models are chosen by purpose (chat, checks, compaction, writing code),
  with an explicit order and an optional reasoning effort.
- The agent builds a module from source through one builder interface and
  installs it after the owner's press; versions are journaled with who,
  why, which commit and how each version behaved.
- Modules own cells; the agent and the owner can list cells and read
  their history.
- The runner runs modules on cron and as agent tools, supervises them and
  escalates failures to the agent.
- The `deye-reader` example is built and installed this way and runs on a
  Railway service next to the current bot.

Non-goals:

- The model writing module code (P3, spec 9.4).
- A separate builder worker (P2); the interface is ready for it.
- Load shedding, switch cells written by modules, flows linking modules.
- Hot reload of config sections other than modules.

Anti-goals:

- A module binary the agent did not build from source in its workspace.
- Text written by a module (an error message, a manifest field) reaching
  the model or a reply (ADR-0041).
- A secret value in `ganglion.yaml`.

## 3. Constraints

- Phase P1: one process, core and runner together; no NATS.
- The node idles in its memory budget (spec 7.5, ADR-0056); a build is a
  separate process and may take more for a minute.
- Accepted decisions stand: ADR-0014 (cells), ADR-0023 (JSON Schemas for
  YAML), ADR-0033 (workspace git), ADR-0039 (command sandbox), ADR-0041
  (module install and the trust boundary).

## 4. The node config

### 4.1 One file, three things outside it (owner)

The node reads `ganglion.yaml`. Outside it stay:

- the config path: `--config` or `GANGLION_CONFIG`, default `ganglion.yaml`
  beside the database;
- the database path (`--db`) and the roles (`--roles`);
- secrets, read from the environment or `NAME_FILE` (package `secrets`).

No file means defaults, as a node without variables behaves today (an
echo bot without models); tests rely on it.

### 4.2 Layout (owner)

```yaml
# yaml-language-server: $schema=schemas/node.schema.json
node:
  memory_limit: 100MiB      # GOMEMLIMIT; absent → 80 % of the container's limit
  tz: Europe/Kyiv
log:     {level: info, dir: logs}
metrics: {addr: 127.0.0.1:9464}          # or off
telegram:
  token: env:TELEGRAM_BOT_TOKEN
  api_url: https://api.telegram.org
  admin: telegram:user:123
  reply: auto                             # auto | always | off
  rich: true
  reactions: {after: 8s, dwell: 3s}
agent:
  workspace: workspace
  languages: languages
  session_idle: 6h
  limits: {daily_budget_usd: 1.0, daily_turns: 300, daily_tokens: 1000000}
  search: {mode: auto, brave_key: env:BRAVE_API_KEY}
sandbox: {deny: [], hide_names: false}
models: …                                 # section 5
modules:                                  # written by module.install, section 6
  - name: local/deye-reader
    config: {inverter_sn: "2306…"}
    # off: true                           # written by /modules off
```

Rules (owner):

- **`off` turns a thing off; a missing key means the default.** `limits:
  off` turns all limits off; `limits: {daily_turns: off}` one of them.
  `metrics.addr: off` and `session_idle: off` already work this way.
- **Secrets are references.** A field that takes a secret holds
  `env:NAME`; the node reads `NAME` or the file `NAME_FILE`. A literal in
  such a field fails the start, so a token cannot reach git when the file
  becomes a repository in M6. Module secrets are not in the file at all:
  the manifest names them and the node reads them from the same source.
- **Strict parsing.** An unknown key fails the start. Errors name the path
  and the reason: `ganglion.yaml: agent.limits.daily_turns: must be 0 or
  more`.
- **A schema with descriptions.** `schemas/node.schema.json` gives every
  field a description, a type, a default and the allowed values, so an
  editor shows them on hover and marks errors; a test keeps it in step
  with the code (ADR-0023). `ganglion.example.yaml` at the root (beside `.env.example`) comments
  every field.
- **Old variables fail the start.** `GANGLION_TZ`, `GANGLION_DAILY_TURNS`,
  `GANGLION_MODELS_FILE`, `key_env` and the rest are no longer read. A
  node that finds one stops with a hint (`GANGLION_TZ is no longer read:
  set node.tz in ganglion.yaml`), so a stale Railway variable cannot leave
  a node silently on defaults.
- **What a `modules:` line holds** (call): the name, the module's config
  and whether it is off. Which version runs, and its files, stay in the
  module store of ADR-0041 (`<data>/modules/<name>/<sha256>.wasm` and
  `module_versions`), so the file never names a path the store owns.
- **The node edits its own file carefully** (call). `module.install` and
  `/modules config|off|on|remove` change only their module's line,
  through the YAML node tree so the operator's comments and order stay,
  and write a temporary file renamed over the old one, so a crash leaves
  either the old file or the new one.
- **Variables that stay** (call): `GANGLION_CONFIG`, `GANGLION_DB` (the
  `--db` flag's variable), `OTEL_EXPORTER_OTLP_ENDPOINT`, the bridge's
  per-run `GANGLION_MCP_TOKEN`, and the test tools' variables
  (`GANGLION_LIVE_*`, `GANGLION_ACCEPT_OUT`, `GANGLION_MEASURE*`,
  `GANGLION_CRASH_*`). Every other `GANGLION_*` that held a setting
  before M4 fails the start.
- **No chats or topics section in M4** (call). They served load-shedding
  notices; module failures reach the owner through the agent.

### 4.3 When changes apply (owner)

- Modules: install, update, rollback, config, off/on and remove apply at
  once, without a restart (R-RUN-8).
- Every other section applies at the next start. `/restart` (admin) ends
  the node cleanly; Railway or systemd starts it again, and delivery
  loses nothing (M2).

### 4.4 Memory limit

`node.memory_limit` sets `GOMEMLIMIT`; without it the node takes about
80 % of the container's limit (cgroup v2 `memory.max`, v1
`memory.limit_in_bytes`), else none (R-RUN-6, ADR-0056). A container sized
for builds (about 1 GB) should set `node.memory_limit` explicitly, or the
node's heap would be allowed 800 MB.

## 5. Models by purpose (owner)

Tiers (`smart`, `cheap`) and "cheaper first within a tier" give way to
purposes with an explicit order:

```yaml
models:
  providers:
    - {name: claude, type: claude-cli}
    - {name: openrouter, type: openai, base_url: https://openrouter.ai/api/v1, key: env:OPENROUTER_API_KEY}
  models:
    - {id: sonnet, provider: claude, name: sonnet, tools: true, context: 200000}
    - {id: haiku,  provider: claude, name: haiku,  tools: true, context: 200000}
    - {id: opus,   provider: claude, name: opus,   tools: true, context: 200000}
  purposes:
    chat:    [{model: sonnet, effort: medium}, {model: haiku}]
    check:   [{model: haiku, effort: low}, {model: sonnet, effort: low}]
    compact: [{model: haiku}]
    code:    [{model: opus, effort: high}, {model: sonnet, effort: high}]
```

- **The purposes are a fixed list in code:** `chat` (turns and scheduled
  agent tasks), `check` (short yes/no decisions such as the clean-context
  check), `compact` (history compaction), `code` (writing modules and
  skills); `translate` (fixed texts into a language with no file,
  R-LANG-5) was added when the plan found that caller. An unknown purpose
  fails the start; `check`, `compact` and `translate` use `chat`'s list
  when absent, `code` has no default. Every place that calls a
  model names its purpose.
- **The order is the preference.** The next entry is taken when one is
  out of limits, answers 429 or times out; cooling and limits work as
  today.
- **`check` keeps a fallback** (call): ADR-0047's clean-context check
  fell back to Sonnet when Haiku was out of limits, and the example keeps
  that. A purpose whose list is used up fails closed, as today.
- **`effort` is optional.** It is passed to providers that take one
  (`reasoning_effort` on OpenAI-compatible APIs); whether the claude CLI
  takes one is checked during the build. A provider that cannot take an
  effort set in the file fails the start; if the CLI cannot, the example
  files drop `effort` from its models.
- **`code` is checked but unused in M4**; the agent starts writing modules
  in P3. Without it, P3 answers "no model to write code" rather than
  picking one.
- A skill's frontmatter `tier` becomes `purpose`.
- This closes the spec's open decision "Choosing models by purpose" for
  roles; embeddings and vision stay in "Not now".

## 6. Modules built by the agent (owner)

### 6.1 Where modules come from

- `ganglion/…`: the agent's base modules, embedded and updated only from
  the project's releases (ADR-0041). Not listed in the config.
- `local/…`: modules of the owner's system. Only one way in: the agent
  builds the source in its workspace and the owner approves the install.
  No file in chat, no URL, no ready `.wasm`.

Sources live in `workspace/modules/<name>/` (the workspace is a git
repository, ADR-0033): code, manifest, `Cargo.toml` or `go.mod`. In M4 the
agent copies the `deye-reader` example there with a host command (an
approval, M3d); in P3 it writes the source itself.

### 6.2 The builder interface (owner)

```go
type Builder interface {
    // Can reports the languages this builder can build, with versions.
    Can(ctx context.Context) (Abilities, error)
    // Build compiles a module's source into one .wasm; the log is for the agent.
    Build(ctx context.Context, src Source) (Artifact, Log, error)
}
```

- M4 has one implementation, `local`: the node builds. Every build runs
  in the M3d command sandbox (no node secrets, a time limit) with its
  target directory in a temporary folder (`CARGO_TARGET_DIR`) removed
  afterwards: one `.wasm` comes out and no build garbage stays.
- `Can` checks what the machine has: `cargo` with `wasm32-wasip1`,
  `tinygo`, `go`. A module in a language no builder can build gets "no
  builder for Rust on this node".
- A worker marked `builder` (P2) is a second implementation of the same
  interface; the owner and the agent see no difference.
- Dependencies come from crates.io (or the Go proxy) pinned by the lock
  file's checksums; the build runs with `--locked` (or `-mod=readonly`), so
  it never resolves a version the lock file does not name.
- **`module.build` asks no press** (call): it reads the workspace, writes
  only its temporary folder and runs in the sandbox without the node's
  secrets; the dependencies' build scripts run there too. What runs in
  the node is guarded by the install press, which names the checksum of
  exactly the artifact the build made.
- This reverses ADR-0041's "the node does not compile modules" for
  `local/…` modules only; built-in modules still come only from releases.

### 6.3 Build, approve, install

1. The owner asks: "build and install deye-reader".
2. `module.build` finds the language, checks `Can`, builds. A failure
   returns the build log to the agent, which says what is wrong.
3. `module.install` checks, then asks the owner:
   - the name is `local/…`;
   - every secret the manifest names is set;
   - every required config key is given;
   - no cell the module would create belongs to another owner;
   - the module loads and its `health`, if exported, passes.

   ```
   Install local/deye-reader (version 3, 161 KB, a3f9…)?
   • Network: eu1-developer.deyecloud.com
   • Secrets: DEYE_APP_ID, DEYE_APP_SECRET, DEYE_EMAIL, DEYE_PASSWORD
   • Creates cells: deye-reader.soc, deye-reader.grid, deye-reader.sample
   • Reads cells: —
   • Runs: every 30 min; agent tool deye-reader.status
   • Limits: 32 MiB, 20 s a call
   [Install]  [Reject]
   ```

   An update with wider rights than the active version opens with the
   widening worded on its own line ("New: network api.example.com"), as
   R-RUN-8 requires.
4. The press installs and starts the module at once and writes its line
   under `modules:` in `ganglion.yaml`.

Owner commands, without a model: `/modules`, `/modules <name>` (card and
journal), `/modules config <name> key=value`, `/modules off|on <name>`,
`/modules rollback <name>`, `/modules remove <name>` (its cells stay,
without an owner).

### 6.4 The journal of improvements (owner)

Every version stays a row of `module_versions` for good (the files stay
two: active and previous). A row adds:

- who built it, at whose request;
- the note "why", written by the agent at build time (only the agent's or
  the owner's words; never text from the module or its manifest);
- the workspace commit the module was built from;
- counters: runs, failures, exceptions, the last failure's kind and time.

```
local/deye-reader — running since 12.11.2026
v3  14.11  "temperature arrives as a string"   1,340 runs, 0 failures  ← active
v2  13.11  "retry after 429"                       96 runs, 4 failures
v1  12.11  first install                           48 runs, 11 failures
```

### 6.5 The language of a module (owner)

Rust by default for small system modules (HTTP, JSON, arithmetic);
TinyGo when Go code already exists and TinyGo compiles it; standard Go
only when TinyGo cannot and a rewrite does not pay. The P0 numbers for
the same Deye logic: Rust 161 KB and 20 MiB, TinyGo 669 KB and 25 MiB,
Go 3.6 MB and 41 MiB. On 32-bit ARM and RISC-V wazero interprets, which
widens the gap. Recorded in an ADR and in spec 9.4, where the agent's
module-writing skill will read it.

## 7. Cells as registers (owner)

A cell is a named register in the node's database: a `sensor` holds a
reported value, a `switch` holds desired and reported; every change is
kept in its history (ADR-0014).

### 7.1 Ownership

The link between a module and its cells is in the manifest; the config
has no cells section.

```yaml
cells:
  report:
    soc:    {kind: sensor, about: "Battery state of charge, %"}
    grid:   {kind: sensor, about: "Grid present: true/false"}
    sample: {kind: sensor, about: "The whole reading, 12 fields"}
  read: []                  # other modules' cells, by full name
```

- Install creates `deye-reader.soc`, `deye-reader.grid` and
  `deye-reader.sample` and records the owner `local/deye-reader`. A name
  owned by another module refuses the install.
- A module names its own cell without the prefix: `cell.report("soc",
  78)`; the node adds `deye-reader.` and checks the manifest and the
  owner. Whatever name a module passes becomes its own name plus a dot, so
  it cannot address another module's cell for writing.
- Reading another module's cell needs it in `cells.read` by full name,
  shown in the install window. This is the link future flows will use.
- Removing a module keeps its cells and history without an owner:
  nobody writes, anyone allowed reads. Only a module of the same full name
  (`local/deye-reader`) takes them back, and its install window says so
  ("takes over 3 existing cells with their history").
- **Reserved names** (call). The prefix is the name without `local/`, so
  a local module may not be named like a built-in module (`html2md`), a
  built-in tool group (`cells`, `module`, `modules`, `memory`, `host`,
  `web`, `schedule`, `message`, `skill`, `metrics`, `config`) or a core
  cell family (`request`). The list lives in code next to the tool
  registry; the install refuses a reserved name.
- The agent reads any cell; it never writes a sensor.
- **Only strings taint** (owner). A module's tool output and its cells'
  values are outside content only where they are strings: numbers,
  booleans and null never taint a session, any string taints it like a
  fetched page (ADR-0049). `about` is shown to the owner in `/cells`,
  never to the model, which gets the cell's name and kind. Deye's values
  are all numbers, so asking about the battery taints nothing.
- **The contract is checked** (owner). The manifest declares the shape of
  each tool's output and the type of each cell's value (number, boolean,
  a string from a fixed list, a free string; objects of these). The node
  checks every answer and every `cell.report`; a mismatch is a format
  error: dropped, counted as a failure, and `module.format` reaches the
  agent with the node's description (`deye-reader.status: field soc:
  expected number, got string`), never the value, as context to revise
  the module (R-RUN-14).
- **Modules hand over contract data** (owner). A module interprets what it
  fetched and returns only what its tools and cells declare; the model
  works after it on prepared data, or not at all. The module-writing skill
  of P3 carries this as a mandatory instruction next to "do not break the
  system you run on" (spec 9.4).
- Tools follow the same rule: the manifest declares `tool: status`, the
  agent sees `deye-reader.status`, so a module's tool can collide neither
  with a built-in tool nor with another module's.

### 7.2 Host functions (call)

`cell.report` and `cell.get` in M4. `cell.set` (a switch's desired value)
waits for the first module that needs it.

### 7.3 Seeing cells (owner)

- `cells.list` for the agent: every cell with kind, owner, `about`,
  value and age.
- `cells.get` with an optional period returns history, condensed: a
  sample of points plus min, max and mean, so a night of readings does
  not flood the context.
- `/cells` for the owner, without a model: values with their age, and
  under each cell a button with its latest changes. It works when the
  model is out of limits.

## 8. The runner in the node

### 8.1 Triggers

- **Cron.** Each `cron:` line of a manifest becomes a scheduler job of the
  kind "module" (the M3b scheduler: time zone, `cron_runs`, a missed slot
  runs once after downtime). Its event goes to the runner, not the
  agent; no model is involved.
- **Tool.** The agent calls `deye-reader.status` through the policies like
  any tool, class read, no press.
- **One instance at a time.** A tool call that arrives during a cron run
  waits, up to the module's time limit.

### 8.2 `init` and `health` (call)

Both optional (spec 7.5). `init` runs when the module loads; a failure
keeps it stopped, with the reason in `/modules`. `health` runs at install
(part of the checks before the press), when the owner opens the
module's card and when the agent calls `module.status`. It checks what a
module can check without network (the Deye reader: a serial number that
looks like one, its secrets set), so a placeholder is caught at install
and shown on the card.

### 8.3 Supervisor (call)

- A failure is a trap, an exceeded memory or time limit, or the module's
  own error answer. After a trap the instance is recreated (R-RUN-2).
- No retries inside a run; the next attempt is the next slot.
- **Five failures within an hour disable the module** and raise
  `module.crashloop`. The owner turns it back on (`/modules on`) or a new
  version does.
- Failures feed the version's counters and `/status`.

### 8.4 Escalation to the agent (call)

- The manifest's `escalate` list says which kinds call the model
  (`deye-reader`: `module.exception`, `auth_failed`); `module.crashloop`
  and `module.format` always do.
- Only a run nobody watched escalates: a cron run, or `init` at load. A
  tool call that fails inside a turn does not: the agent has the node's
  words for it in that turn, and a second turn would repeat them (owner,
  2026-11-10). It still counts toward the crash loop and the journal.
- Every failure the model sees, in a tool's error or an escalation's
  note, ends with the node's fixed hint for its kind (`hint.*` texts:
  refused login → check the secrets and settings with `module.status`;
  stale source; a defect in the module; the log has the details; time
  limit; trap), so the model does not guess the cause.
- The event arrives as a turn in the owner's private chat: "module
  deye-reader: auth_failed, version 3, 14:30". The agent explains and
  proposes.
- The model sees the kind and the node's own facts, never the module's
  text; the full text goes to the log, readable through `ganglion mcp`.
- One event of one kind per module until it recovers (owner ruling of
  2026-11-10, replacing "per hour"): a module that keeps failing below the
  crash-loop threshold does not burn tokens every 30 minutes. A successful
  run, a new version, a rollback or turning the module on closes the
  episode; the open episodes are stored, so a restart does not repeat one.

### 8.5 Cell ticks

`cells.Reconcile` at start and every 5 minutes; `ExpireDue` every 10
seconds (from the M1 review).

### 8.6 Telemetry (owner)

Telemetry over NATS is P2 (R-OBS-9). In P1 modules, the runner and the
`local` builder run inside the node, so their numbers join the node's own
OpenTelemetry metrics (`/metrics`, the promtop dashboard, `/metrics` in
Telegram, `/status`). Nothing in M4 is thrown away when a builder or a
runner moves to a worker: the same instruments then travel as snapshots
over the bus. M4 adds:

| What | Metric |
|---|---|
| Module runs | `ganglion.module.calls` gains the version and the trigger (cron or tool) |
| Supervisor | disables by `crashloop`; events to the agent by kind, and how many the once-per-episode rule held back |
| Builds | count and result by language, duration, the build process's peak memory (it sizes the Railway container) |
| Installs | installs, refusals by reason, rejected presses |
| Cells | reports by owning module; the age of each cell's last value, which shows a module that stopped writing |

`/status` gains a line: modules running and disabled, the last build and
its result.

The owner has no telemetry collector yet. OTLP traces stay as they are:
off unless the standard `OTEL_EXPORTER_OTLP_ENDPOINT` is set. That
variable is the one exception to "the file is the single source": it is
OpenTelemetry's own setting, not one of ganglion's, and it gets no key in
`ganglion.yaml` until a collector exists.

## 9. The `deye-reader` example

- The Rust module (`modules/deye-rs`) moves to
  `examples/modules/deye-reader/` with a manifest in the new form. The Go
  module (`modules/deye`) stops being a module: it moves to the runner's
  test data, as the Go glue's conformance module and as the reference a
  test compares the Rust build with on a recorded DeyeCloud response.
- **Cells:** `soc`, `grid`, `sample` (section 7.1).
- **Polling every 30 minutes** on cron (owner). The 5-minute cadence
  without grid served load shedding and leaves with it.
- **HTTP 429:** the module keeps "not before" from `Retry-After` in its
  state and does not call the cloud until then.
- **Stale data** (a reading older than 20 minutes) is reported once per
  episode.
- **Login failure:** no second login with the same credentials (the
  account gets locked; the code already does this); `auth_failed` is
  raised.
- **`deye-reader.status`** reads fresh under the same 429 rule; when the
  cloud cannot answer it returns the last cell values with their age.
- Tests on the replay server of `tools/modbench`: recorded responses, 429
  with `Retry-After`, stale data, a refused login.
- `make example-deye` builds it the same way the builder does, into a
  temporary target folder removed afterwards (owner); the repository keeps
  source and manifest only.

## 10. Deploy

- **Image:** the `ganglion` binary, `git`, Rust with `wasm32-wasip1`, an
  unprivileged `USER` (ADR-0039). About 1–1.5 GB with the toolchain.
- **Volume:** the database, the workspace (module sources inside), the
  installed modules, logs and `ganglion.yaml`. The config lives on the
  volume, not in the image: the agent changes it in M6.
- **Memory:** the container limit leaves room for a build (about 1 GB);
  `node.memory_limit` is set explicitly (section 4.4).
- **A Railway service** next to the current bot, with the test bot's
  token; `ganglion mcp` checked over `railway ssh`; for a few days the
  module's readings are compared with `deye.py` (R-LS-4, kept as a
  migration check).
- The model provider on Railway (the claude CLI needs installing and a
  login; OpenRouter does not) is chosen at deploy time.

## 11. Acceptance of M4

Scripted (chatlab and `make accept`), then live on the test bot:

1. A bad `ganglion.yaml` (unknown key, literal secret, old variable set)
   fails the start with a message naming the path.
2. In a container limited to 256 MB without `node.memory_limit`,
   `GOMEMLIMIT` is about 205 MB.
3. "Build and install deye-reader": the build runs in the sandbox, the
   install window lists the rights, the press starts the module, and no
   build folder remains.
4. A ready `.wasm` sent to the bot is not installed.
5. On cron the module writes `deye-reader.soc` and `deye-reader.grid`
   without a model call.
6. "How is the battery?" answers through `deye-reader.status`; "how did it
   discharge overnight?" through the history.
7. `/cells` lists the cells without a model.
8. A probe module that fails five times in an hour is disabled, and the
   agent tells the owner once.
9. A second version with a wider network list shows the widening on its
   own line; the journal lists both versions with notes and counters.
10. A turn in chat uses the `chat` list, the clean-context check the
    `check` list, in the configured order.

## 12. Spec and documents to change first

- **Spec:**
  - section 8 and R-LS-1…3 move out of P1 to the far part, next to 9.6,
    reworded as flows of ready modules linked through cells with a visual
    editor; R-LS-4 stays as a migration check;
  - 10.2 acceptance and the gate: "the Deye module runs on cron without
    the model; its versions and failures are journaled" replaces "two
    weeks without the model";
  - 7.5: modules `local/…` only from source through a builder; the
    journal; cells owned by modules; prefixes of cells and tools;
  - new requirements R-CFG-* (the config file) and for cells (`cells.list`,
    history, `/cells`);
  - 7.3: models by purpose replace tiers; the open decision closes;
  - 9.4: the language rule.
- **ADRs** (next numbers from 0058):
  1. Load shedding leaves P1; the gate without the two-week clock (a
     change to the phase plan).
  2. The node config file as the single source; secrets as `env:NAME`
     (closes D-3).
  3. Models by purpose with an explicit order and an effort (supersedes
     the tier part of ADR-0047's practice).
  4. Modules `local/…` built from source by a builder; the version journal
     (extends ADR-0041).
  5. Cells owned by modules; prefixes of cell and tool names (extends
     ADR-0014).
  6. The language of a module: Rust, TinyGo, Go.
- **Phase plan:** M4 tasks rewritten (below), M5 removed, the board's M4
  cards replaced.

| # | Task |
|---|---|
| 4.1 | Config file: skeleton, schema, `env:NAME`, memory limit, `modules:` section |
| 4.2 | Every old setting and `models.yaml` into the file, models by purpose; old variables fail the start; the lab and scripts on example files |
| 4.3 | Module cells: host functions, ownership, `cells.list`, history, `/cells` |
| 4.4 | Runner in the node: module jobs, module tools, `init` and `health`, supervisor, escalation, cell ticks |
| 4.5 | Builder (`local`), `module.build`, `module.install` with the rights window, the journal, `/modules` commands, `/restart` |
| 4.6 | The `deye-reader` example in Rust: cells, 429, stale data; the Go reference in tests |
| 4.7 | Deploy on Railway and comparison with `deye.py` |
| 4.8 | Acceptance: lab scenarios and the live check on the test bot |

## 13. Left to the builder

- Whether the claude CLI takes an effort, and how.
- The exact JSON shape of the condensed history.
- The scheduler job kind for modules and how a missed slot is caught up
  for a module whose run is already in flight.
- The exact form of the `examples/ganglion.*.yaml` files the lab and
  scripts use.
