# P1 · M4c: reflex modules — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** a module runs when cells it listens to change and writes to the owner's private chat
within a daily limit, with no model; the agent writes such a module's source itself from a clean
session, builds it, fixes its own compile errors and ends at the install window. The live proof is
`deye-alerts`, written by the agent from one message of the owner's.

**Architecture:**

- **Existing parts that change:**
  - `runner`: manifest triggers gain `cell`, capabilities gain `notify`; a `notify` host function
    behind a `NotifyPort` in `Deps`; `pdk`, `pdk/guest` and the probe module follow.
  - `modstore`: rights gain the cells a module reacts to and its `notify` limit; the install
    refuses a trigger loop between local modules; a table counts messages per module per day.
  - `modhost`: a run carries its identity and chain depth in its context; the cell port collects
    the cells a run wrote; after a run, one `module.trigger` event per listening module goes into
    the inbox; `RunTrigger` runs a listener; the notify port counts and puts `module.notify`.
  - `event`: kinds `module.trigger` and `module.notify` with their payloads.
  - `agent`: `Handle` runs `module.trigger` before any conversation state (no turn); a
    `module.notify` event writes the node note into history and the message into the outbox in
    the same transaction; `/modules` cards show "Reacts to", "Feeds" and messages today.
  - `builder`: `SourceDigest` (the same walk as `CopySource`) and `OwnDiagnostics` (rustc's
    diagnostics about the module's own files).
  - `tool/builtin`: `module.write`, `module.read`; provenance records; `module.import` records
    provenance; `module.build` answers clean for a folder that matches its record; the window
    and `module.status` show the new rights.
  - `skills/builtin/module-writing/SKILL.md`: the module-writing skill.
  - `examples/modules/reflex-template` (Rust): the reflex template.
- **No new package, no new Go module, no new crate.**

**Tech stack:** Go 1.27, wazero, SQLite, Rust stable with `wasm32-wasip1` for the template.

**Spec:** 7.5 (R-RUN-1, 9, 11–17), 7.6 (R-POL-6); the [M4c design](p1-m4c-reflexes-design.md);
[ADR-0065](../adr/0065-cell-triggers-through-the-inbox.md),
[ADR-0066](../adr/0066-notify-capability.md), [ADR-0067](../adr/0067-agent-writes-module-source.md);
[ADR-0049](../adr/0049-three-gaps-where-outside-text-and-private-data-cross.md),
[ADR-0061](../adr/0061-local-modules-built-from-source.md),
[ADR-0062](../adr/0062-cells-owned-by-modules.md). Read them with this plan. M4b
([plan](p1-m4b-modules.md)) is done and merged.

## Global Constraints

- All documentation, comments and doc comments are in English; the [conventions](../conventions.md)
  apply (constructors, no `nil` collaborators, named types for closed sets, small functions, the
  linter's limits, an injected clock, no sleeping in tests).
- Work on branch `m4c-reflexes`. Commit after each task with `make lint test` green; stage only
  the task's own files by path (`git add <path>…`, never `git add -A`). Push after each task.
- Architecture docs change in the same commit as the code (`05-code-map.md` rows and dependency
  rules; `03-components.md` and `04-runtime.md` when components connect differently).
- Only `config` builds and connects components; domain packages never import `samber/do`.
- **A trigger never calls a model.** A `module.trigger` event creates no turn and reads no
  conversation state.
- **Text a module wrote never reaches the model** (R-RUN-13): a `notify` text goes to the owner
  only; history gets the node's fixed note without it.
- **`module.write` never writes from a tainted session**, and nothing it writes leaves
  `workspace/modules/<name>/`.
- **A build answer is clean only when the folder's digest equals the node's provenance record**;
  in every other case it stays outside content, as today.
- **No secret value in a log, an error, a journal row, a message, the window or a fixture.**
- Exact numbers: `notify.per_day` 1–100; text cut at 1000 characters with "…"; chain depth above 4
  stops; `module.write` at most 256 KB a file, 16 MB and 200 files a module.
- Tests that need Rust skip with a message naming what is missing; CI has Rust. Logic is tested
  without Rust through fakes and the Go probe module.

## Rulings this plan makes (call; the owner may overturn them in review)

1. **`notify` goes inbox → agent → outbox.** The notify port puts a `module.notify` event into the
   owner's conversation; the agent's handler writes the node note into history and returns the
   message, and the queue stores both in one transaction. The design says "through the outbox":
   it still is, and the note and the message cannot drift apart.
2. **A module's message carries a tainted origin.** Replying to it or quoting it brings module text
   into the session as outside content (the reply context of R-DLV-13 already handles a tainted
   origin).
3. **Run identity.** A cron run is identified by its schedule event's idempotency key (the slot), a
   trigger run by its `module.trigger` event id, a tool run by the call's key. `init` and `health`
   runs have none: `notify` there returns "not in this run" and sends nothing, so an install's
   health check never writes to the owner.
4. **`module.trigger` events use the conversation `module:<listener>`**, so the queue serialises a
   listener's events, and they are handled in `Agent.Handle` before `begin`.
5. **The loop check reads the active versions of every local module, off ones included**, from
   `modstore`, at plan and at install.
6. **The "chain deeper than 4" line is once per active build, in memory**: a restart may repeat it
   once. The escalation episodes are not used, since a successful run clears them.
7. **Provenance lives in `<data>/modules/provenance/<name>.json`** (`{"digest","origin","at"}`),
   written atomically, beside the builds, outside the workspace and the command sandbox's reach.
   The digest walk is `builder.SourceDigest`, beside `CopySource`, so both skip the same files.
8. **The clean log** keeps blocks that start with `error` or `warning` and name a path under
   `src/`, or `Cargo.toml`, or `manifest.yaml`; it drops `warning: <crate>@<version>:` lines
   (dependencies' build scripts) and everything else from cargo. When nothing is kept, the
   answer says "the build failed; no diagnostic names the module's own files".
9. **`module.write` is `ClassRead`, `PolicyAuto`, `AdminOnly`**, and refuses in `Run` when
   `c.Tainted`, as `persona.propose` does. `ClassSideEffect` would ask for a press, which the
   owner ruled out.
10. **The skill is a built-in, `skills/builtin/module-writing`.** The design says "in the agent's
    workspace"; nothing seeds the workspace's skills, and a built-in ships with the binary and
    cannot be shadowed by a workspace skill of the same name.
11. **Messages are counted per module per day in `modstore`** before the event is put; a crash
    between the two can count one message that was never sent.
12. **One `cell` trigger per manifest**, listing full cell ids.
13. **`notify` codes:** `0` sent, `-1` not granted, `-2` over today's limit, `-3` not in this run.

## Review Focus

1. **A model turn on a reflex path**: any `module.trigger` or `module.notify` that reaches a
   provider, or a module's text in the model's messages.
2. **A write outside the module's folder**, or a `module.write` that succeeds in a tainted session.
3. **A clean build answer for a folder the node's tools did not leave**, or dependency output in a
   clean answer.
4. **Flooding**: a trigger storm, a loop, a module over its limit, a repeated run sending twice.
5. **Restart safety**: a trigger or a notify event put before a crash is handled once after it.

## Files

| File | Change |
|---|---|
| `runner/manifest.go`, `runner/manifest_test.go`, `schemas/module-manifest.schema.json` | `cell` trigger, `notify` capability |
| `runner/runner.go`, `runner/notify.go` (+test), `runner/runnertest/*` | `notify` host function, port, suite |
| `pdk/pdk.go`, `pdk/guest/guest.go`, `runner/runnertest/probe/*` | `Notify` for guests, probe op |
| `modstore/local.go`, `modstore/chains.go` (+test), `modstore/notify.go`, `modstore/migrations/005_notify.sql` | rights, loop check, daily count |
| `event/event.go`, `event/message.go` | kinds and payloads |
| `modhost/run.go`, `modhost/trigger.go`, `modhost/notify.go` (+tests), `modhost/cells.go`, `modhost/tools.go`, `modhost/admin.go` | run context, fan-out, notify port, card |
| `agent/agent.go`, `agent/module_trigger.go`, `agent/module_notify.go` (+tests), `agent/scheduled.go`, `agent/modules.go` | routing, note, cron run id, card lines |
| `config/modhost.go`, `config/agent.go` | wiring (time zone, ports) |
| `texts/texts.go`, `texts/lang/*` | window, card and node lines |
| `builder/digest.go`, `builder/diagnostics.go` (+tests) | digest, clean log |
| `tool/builtin/module_write.go`, `module_read.go`, `provenance.go` (+tests), `module_import.go`, `module_build.go`, `module_window.go`, `module_status.go`, `module.go` | tools, provenance, answers |
| `skills/builtin/module-writing/SKILL.md` | the skill |
| `examples/modules/reflex-template/*`, `.github/workflows/ci.yml` | the template and its CI |
| `tools/chatlab/scenarios/m4c-*.yaml`, `tools/chatlab/scenarios/modules/*.yaml` | acceptance |
| `docs/architecture/03-components.md`, `04-runtime.md`, `05-code-map.md` | views |

---

### Task 1: Manifest — `cell` triggers and `notify`; rights and the loop check (R-RUN-15, 16)

**Files:**
- Modify: `runner/manifest.go` (`Trigger`, `Capabilities`, `checkTriggers`, `checkV2`),
  `runner/manifest_test.go`, `schemas/module-manifest.schema.json`
- Modify: `modstore/local.go` (`Rights`, `baseRights`, `widening`, `checkBuild`, `PlanLocal`,
  `InstallLocal`)
- Create: `modstore/chains.go`, `modstore/chains_test.go`

**Interfaces:**
- Produces:

```go
// runner
type Trigger struct {
	Cron   string   `yaml:"cron,omitempty"`
	Tool   string   `yaml:"tool,omitempty"`
	Cell   []string `yaml:"cell,omitempty"` // full cell ids it listens to (R-RUN-15)
	About  string   `yaml:"about,omitempty"`
	Output *Shape   `yaml:"output,omitempty"`
}
type Notify struct {
	PerDay int `yaml:"per_day"` // 1..MaxNotifyPerDay
}
// Capabilities gains: Notify *Notify `yaml:"notify,omitempty"`
const MaxNotifyPerDay = 100
// Listens is the union of the manifest's cell triggers, sorted ("" none).
func (m Manifest) Listens() []string

// modstore: Rights gains
//   Reacts []string // cells its triggers listen to
//   NotifyPerDay int // 0 = not granted
// A widening lists new Reacts and a higher NotifyPerDay.
var ErrLoop = errors.New("modstore: the triggers would form a loop")
// loopOf returns the loop the module's listens would close among the active local
// modules (e.g. ["a","b","a"]), nil when none. Edges run from a cell's owner (the id's
// prefix) to each module that listens to it.
func loopOf(module string, listens []string, others map[string][]string) []string
```

- [ ] **Step 1: Failing tests** in `runner/manifest_test.go`: a manifest with
  `- cell: [p.soc]` and `cells.read: [p.soc]` parses and `Listens()` is `[p.soc]`; a trigger cell
  missing from `cells.read` fails with `trigger %d: cell %q is not in cells.read`; a trigger cell
  of the module itself (`<name>.x`) fails with `trigger %d: a module cannot listen to its own cell
  %q`; a trigger setting two of cron, tool, cell fails with "exactly one of cron, tool, cell"; a
  second `cell` trigger fails; `notify: {per_day: 0}` and `{per_day: 101}` fail with
  `notify.per_day must be 1..100`; the schema test passes with the new fields.
- [ ] **Step 2: Failing tests** in `modstore/chains_test.go` (table): no edges → nil; `b` listens
  to `a.x`, now `a` listens to `b.y` → `[a b a]`; a three-module loop; a module replacing its own
  old version does not loop with itself; and in `modstore` plan tests: `PlanLocal` refuses with
  reason "the triggers would form a loop" and detail "a → b → a"; `Rights.Reacts` and
  `NotifyPerDay` filled; widening lists a new reacted cell and a raised limit, not a lowered one.
- [ ] **Step 3: Run, see them fail.**
- [ ] **Step 4: Implement.** `checkTriggers` counts one of `Cron`, `Tool`, `len(Cell) > 0`;
  `checkV2` checks trigger cells against `Cells.Read` and the module's own prefix. `PlanLocal`
  and `InstallLocal` read the active manifests of the other local modules (off ones included)
  through `s.versions`/`readLocal` and call `loopOf`; the detail joins the loop with " → ".
- [ ] **Step 5: `make lint test`; code map rows for `runner` and `modstore`; commit, push.**

```bash
git commit -m "runner, modstore: cell triggers and notify in the manifest; rights show them; a trigger loop is refused (R-RUN-15, 16)"
```

---

### Task 2: Runner — the `notify` host function (R-RUN-16)

**Files:**
- Create: `runner/notify.go`, `runner/notify_test.go`
- Modify: `runner/runner.go` (`Deps`, `instantiateHost`), `runner/runnertest/*` (suite),
  `pdk/pdk.go`, `pdk/guest/guest.go`, `runner/runnertest/probe/probe.go`

**Interfaces:**
- Produces:

```go
// runner
type NotifyCode int32
const (
	NotifySent    NotifyCode = 0
	NotifyDenied  NotifyCode = -1 // the manifest grants no notify
	NotifyLimit   NotifyCode = -2 // today's limit is used up
	NotifyNotHere NotifyCode = -3 // init and health runs send nothing
)
// NotifyPort sends a module's text to its owner. The run's identity travels in ctx
// (the host sets it); the port decides limits. Text arrives already cut.
type NotifyPort interface {
	Notify(ctx context.Context, text string) NotifyCode
}
// Deps gains Notify NotifyPort; nil means NotifyDenied for every call.
const MaxNotifyText = 1000 // characters
// cutText cuts s to MaxNotifyText characters, ending with "…" when cut, and makes it valid UTF-8.
func cutText(s string) string

// pdk
// Host gains Notify(text string) error.
var (
	ErrNotifyDenied  = errors.New("notify is not granted")
	ErrNotifyLimit   = errors.New("today's notify limit is used up")
	ErrNotifyNotHere = errors.New("notify does nothing in this run")
)
```

Host function, in the existing chain:

```go
NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, p, n uint32) int32 {
	if caps.Notify == nil || m.deps.Notify == nil {
		return int32(NotifyDenied)
	}
	return int32(m.deps.Notify.Notify(ctx, cutText(readString(mod, p, n))))
}).Export("notify").
```

- [ ] **Step 1: Failing tests:** `cutText` (999, 1000, 1001 characters, multi-byte, invalid
  UTF-8); the suite: a probe granted `notify` reaches a fake port with the text and gets its code
  back; one without the grant gets `-1` and the port sees nothing; a 2000-character text arrives
  as 1000 characters ending in "…"; the Go guest maps the codes to the `pdk` errors.
- [ ] **Step 2: Run, see them fail.**
- [ ] **Step 3: Implement**; the probe gets op `notify` (`{"op":"notify","value":"<text>"}`,
  returns the code in its result); `pdk.Input`'s comment lists the trigger `"cell"`.
- [ ] **Step 4: `make lint test`; code map row for `runner`; commit, push.**

```bash
git commit -m "runner: a notify host function behind a port, the text cut at 1000 characters (R-RUN-16)"
```

---

### Task 3: Cell triggers — the run context, the fan-out and the route (R-RUN-15)

**Files:**
- Create: `modhost/run.go`, `modhost/trigger.go`, `modhost/trigger_test.go`,
  `agent/module_trigger.go`, `agent/module_trigger_test.go`
- Modify: `event/event.go`, `event/message.go`, `modhost/cells.go`, `modhost/tools.go`,
  `agent/agent.go` (`Handle`, `Deps`), `agent/scheduled.go`, `config/agent.go`, `config/modhost.go`

**Interfaces:**
- Produces:

```go
// event
const (
	KindModuleTrigger Kind = "module.trigger"
	KindModuleNotify  Kind = "module.notify"
)
type ModuleTrigger struct {
	Module   string   `json:"module"`   // the listener, full name
	Producer string   `json:"producer"` // full name
	Run      string   `json:"run"`      // the producer's run identity
	Cells    []string `json:"cells"`    // the listened cells it wrote, sorted
	Depth    int      `json:"depth"`    // 1 for a trigger from a cron or tool run
}

// modhost
const maxDepth = 4
// run is what a module's run carries in its context: who started it and how deep in a chain.
type run struct {
	ID      string  // "" for init and health
	Trigger trigger // cron | tool | cell
	Depth   int     // 0 outside a chain
	wrote   *written // the cells the run reported, full ids
	sent    *int     // notify calls so far in this run (task 4's key)
}
func withRun(ctx context.Context, r run) context.Context
func runOf(ctx context.Context) (run, bool)
// RunTrigger runs the listener of e once; a module that is not running is a no-op.
func (h *Host) RunTrigger(ctx context.Context, e event.ModuleTrigger, eventID string) error
// RunCron gains the run identity: RunCron(ctx, module, run string) error.

// agent
// Deps gains ModuleTrigger func(ctx context.Context, e event.ModuleTrigger, id string) error;
// ModuleCron becomes func(ctx context.Context, module, run string) error.
```

`triggerCell` is a third `trigger`, `unattended()` true. The fan-out, called after `settle` for
every run that has a run context:

```go
// fanOut puts one module.trigger per running listener of the cells r wrote.
func (h *Host) fanOut(ctx context.Context, l *loaded, r run) {
	cells := r.wrote.list()
	if len(cells) == 0 {
		return
	}
	if r.Depth+1 > maxDepth {
		h.chainStopped(ctx, l) // node line once per sha (ruling 6)
		return
	}
	for _, sub := range h.listeners(cells) { // running modules whose Listens() meet cells
		h.putTrigger(ctx, l.name, sub, r, cells)
	}
}
```

`putTrigger` uses `IdemKey: "trigger:" + sub.name + ":" + producer + ":" + r.ID`,
`Conv: "module:" + sub.short`, `Source: event.SourceModule`. `Agent.Handle` starts with:

```go
if e.Kind == event.KindModuleTrigger {
	return nil, queue.StateUpdate{}, a.moduleTrigger(ctx, e) // no begin, no turn, no state
}
```

`moduleTrigger` decodes the payload (a bad one is logged and acknowledged) and calls
`d.ModuleTrigger`; it returns nil after the run whatever the run did (a failure is the
supervisor's), and an error only when the host could not start it, so the queue retries.

- [ ] **Step 1: Failing tests** (`modhost`, fake clock, probe modules): a producer whose cron run
  reports `p.soc` and `p.grid` puts exactly one event for a listener of both, with sorted cells and
  depth 1; a listener of `p.other` gets none; a run that reports nothing puts nothing; the same run
  identity twice puts one event (queue dedup); a listener that is off gets no event; `RunTrigger`
  passes `Input{Trigger:"cell", Payload:{"cells":[...],"producer":"local/p"}}`; a trigger run at
  depth 4 that writes cells puts nothing and sends the chain line once per sha; a failing trigger
  run counts towards the crash loop and escalates like cron.
- [ ] **Step 2: Failing tests** (`agent`): a `module.trigger` event calls the port and returns no
  outgoing message and no state update, and no provider is called (the fake provider fails the
  test if called); a cron job of kind module passes the schedule event's idempotency key as run.
- [ ] **Step 3: Run, see them fail.**
- [ ] **Step 4: Implement.** `cellPort.Report` adds the full id to `runOf(ctx).wrote` after a
  successful report; `Host.call` takes the run, wraps the context and fans out after `settle`;
  `config` wires `RunTrigger` and the new `RunCron`.
- [ ] **Step 5: `make lint test`; `04-runtime.md` gets the trigger path (a sequence diagram:
  producer run → inbox → `Agent.Handle` → `modhost.RunTrigger`); code map rows; commit, push.**

```bash
git commit -m "modhost, agent: a run's cells start its listeners through the inbox, with no turn; chains stop at depth 4 (R-RUN-15)"
```

---

### Task 4: `notify` — the daily count, the event, the note and the message (R-RUN-16)

**Files:**
- Create: `modhost/notify.go`, `modhost/notify_test.go`, `modstore/notify.go`,
  `modstore/migrations/005_notify.sql`, `agent/module_notify.go`, `agent/module_notify_test.go`
- Modify: `event/message.go`, `event/event.go` (`Outgoing.NoPreview`), `agent/agent.go`
  (`payloads`, `resume`), `modhost/host.go` (`Deps.Location`), `config/modhost.go`,
  `transport/telegram/types.go`, `transport/telegram/transport.go`, `transport/telegram/richsend.go`,
  `texts/texts.go`, `texts/lang/*`

**Interfaces:**
- Produces:

```go
// event
type ModuleNotify struct {
	Module    string `json:"module"`    // full name
	Text      string `json:"text"`      // the module's text, cut; "" for a node line
	Node      string `json:"node"`      // "" | "limit" | "chain": a node line instead of the text
	PerDay    int    `json:"per_day"`
	At        int64  `json:"at_ms"`
	Principal string `json:"principal"`
	Transport string `json:"transport"`
}

// modstore
// CountNotify adds one to the module's count for day and returns the new count and whether
// the limit line was already sent that day.
func (s *Store) CountNotify(ctx context.Context, module string, day time.Time) (n int, limited bool, err error)
func (s *Store) MarkLimited(ctx context.Context, module string, day time.Time) error
func (s *Store) NotifiedOn(ctx context.Context, module string, day time.Time) (int, error)

// event: Outgoing gains NoPreview bool: the transport shows no link preview and sends it
// as an ordinary message, never a rich one. Telegram: sendParams gains
//   LinkPreviewOptions *linkPreviewOptions `json:"link_preview_options,omitempty"`
// with IsDisabled bool `json:"is_disabled"`; sendsRich is false for a NoPreview message.

// modhost: Deps gains Location *time.Location. The port, one per loaded module:
func (p notifyPort) Notify(ctx context.Context, text string) runner.NotifyCode
```

```sql
-- Messages a module sent each day (R-RUN-16, ADR-0066).
CREATE TABLE notify_days (
  module  TEXT NOT NULL,
  day     TEXT NOT NULL,          -- YYYY-MM-DD in the node's time zone
  sent    INTEGER NOT NULL DEFAULT 0,
  limited INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (module, day)
);
```

The port: no run identity → `NotifyNotHere`; count with `clock.DayStart(now, loc)`; over
`PerDay` → put the "limit" line once that day, return `NotifyLimit`; else put
`event.Event{Kind: KindModuleNotify, Conv: l.to.conv, IdemKey: "notify:" + l.name + ":" + r.ID +
:" + strconv.Itoa(*r.sent)}` after incrementing `r.sent`, and return `NotifySent`.

The agent's handler, through `begin` like `moduleEvent`, with no model:

```go
// moduleNotify writes the node note into history and returns the message. The module's text
// goes only into the outgoing message, escaped and prefixed; the model never sees it.
func (a *Agent) moduleNotify(ctx context.Context, t *turn, p event.ModuleNotify) {
	// The name and the time are formatted as moduleNote formats them (module_event.go).
	t.note(fmt.Sprintf("%s%s notified you at %s", moduleNotePrefix, name, clock))
	t.out = append(t.out, event.Outgoing{
		IdemKey: t.event.IdemKey + ":out", Transport: p.Transport, Conv: t.event.Conv,
		Text:      a.notifyText(ctx, t, p), // name + ": " + texts.Escape(p.Text), or the node line
		NoPreview: true,
		Origin:    &event.Origin{Kind: event.OriginSystem, Tainted: true}, // ruling 2; stamp keeps it
	})
}
```

A "limit" or "chain" line writes no history note. New texts: `module.notify_limit` ("{module}
reached its limit of {n} messages today"), `module.chain_stopped` ("{module}: a chain of modules
deeper than 4 was stopped").

- [x] **Step 1: Failing tests** (`modstore`): the count per day; a new day starts at zero in the
  node's zone (a clock at 23:59 and 00:01 Kyiv); `MarkLimited` once.
- [x] **Step 2: Failing tests** (`modhost`, probe op `notify`): a cron run sends one event with
  the key of its slot; the same slot run twice puts one event; the 25th message of a 24-a-day
  module returns `-2` and puts exactly one "limit" event that day, and the 26th puts none; a
  `health` run returns `-3` and puts nothing.
- [x] **Step 3: Failing tests** (`agent`): a `module.notify` event produces one outgoing message
  `deye-alerts: <escaped text>` with a tainted origin, adds the history note without the text,
  leaves the session's taint unchanged, calls no provider; a text with `*bold*` and a link arrives
  escaped; a "limit" event sends the node line and adds no note. Telegram: a `NoPreview` message
  is sent with `link_preview_options.is_disabled` and never as a rich message.
- [x] **Step 4: Run, see them fail. Implement.** Texts in every language catalog.
- [x] **Step 5: `make lint test`; `04-runtime.md` (the notify path), code map; commit, push.**

```bash
git commit -m "modhost, agent: notify writes to the owner within a daily limit; history gets the node's note, never the text (R-RUN-16)"
```

---

### Task 5: The window, the cards and `module.status` show reactions and messages

**Files:**
- Modify: `tool/builtin/module_window.go`, `tool/builtin/module_build.go` (`rightsOf`),
  `tool/builtin/module_status.go`, `modstore/card.go`, `modhost/admin.go`, `agent/modules.go`,
  `texts/texts.go`, `texts/lang/*`, golden files

**Interfaces:**
- Produces: `modstore.Card` gains `ReactsTo []string`, `Feeds []string` (running modules that
  listen to this module's cells), `NotifiedToday int`, `NotifyPerDay int`. Text keys
  `ask.module_reacts` ("Reacts to: {cells}"), `ask.module_notify` ("Writes to you: up to {n}
  messages a day"), `ask.module_new_reacts`, `ask.module_new_notify`, `modules.card_reacts`,
  `modules.card_feeds`, `modules.card_notify` ("Messages today: {n} of {limit}").

- [ ] **Step 1: Failing golden tests:** the window of a reflex lists "Reacts to" and "Writes to
  you" after "Reads"; a version that adds a cell or raises the limit says so under "New:"; the
  `/modules` cards of a producer and its listener show "Feeds" and "Reacts to"; `module.status`
  shows the counts and the cells, never a message text.
- [ ] **Step 2: Run, see them fail. Implement** (escaping through `listed`/`item`, every
  language).
- [ ] **Step 3: `make lint test`; commit, push.**

```bash
git commit -m "modules: the window, the cards and module.status show what a module reacts to and how much it writes"
```

---

### Task 6: The agent writes source — `module.write`, `module.read`, provenance, a clean build answer (R-RUN-17)

**Files:**
- Create: `builder/digest.go`, `builder/diagnostics.go` (+tests), `tool/builtin/provenance.go`,
  `tool/builtin/module_write.go`, `tool/builtin/module_read.go` (+tests)
- Modify: `tool/builtin/module.go` (`NewModuleTools`, `Modules.Provenance`),
  `tool/builtin/module_import.go`, `tool/builtin/module_build.go`, `config/modhost.go`

**Interfaces:**
- Produces:

```go
// builder
// SourceDigest hashes a module folder as CopySource would copy it: regular files in path
// order (skipping the top-level target and .git), each as its relative path, a zero byte,
// its length and its bytes. A symlink or a device is an error.
func SourceDigest(dir string) (string, error)
// OwnDiagnostics keeps rustc's diagnostics about the module's own files (ruling 8).
func OwnDiagnostics(log Log) string

// tool/builtin
const (
	ModuleWriteTool = "module.write"
	ModuleReadTool  = "module.read"
	maxWriteFile    = 256 << 10
	maxWriteModule  = 16 << 20
	maxWriteFiles   = 200
)
type provenance struct {
	Digest string    `json:"digest"`
	Origin string    `json:"origin"` // "example:<name>" | "agent"
	At     time.Time `json:"at"`
}
// Modules gains Provenance string: <data>/modules/provenance.
func (m Modules) recordProvenance(name, origin string) error  // digest now, write atomically
func (m Modules) ownSource(name string) bool                   // digest equals the record
// cleanPath checks a module-relative path: not absolute, no "..", no segment starting
// with ".", not under target/, no symlink on the way inside the folder.
func cleanPath(dir, rel string) (string, error)
```

`module.write {name, path, content}`: `Class: ClassRead, Policy: PolicyAuto, AdminOnly: true`.
`Run`: `c.Tainted` → the error "this conversation holds content from outside, so it cannot write
a module's source; start a new conversation and ask again"; name check (`moduleName`); for a new
module, the folder is created; `cleanPath`; the size limits (the module's total after the write);
write through `writeFile`; `Workspace.Commit(ctx, []string{"modules/<name>/<path>"}, "module
<name>: write <path>")`, restoring the old content (or removing a new file) if the commit fails;
then `recordProvenance(name, "agent")`. Answer `{"written":"<path>","commit":"<short>"}`.

`module.read {name, path?}`: `Class: ClassRead, Policy: PolicyAuto, AdminOnly: true`. Without
`path` it lists the files (as the digest walks them); with it, the file. `External: !ownSource`.

`module.import` records provenance `example:<from>` after its commit. `module.build`:
`own := t.m.ownSource(name)` before the build; `failed` uses `OwnDiagnostics(log)` and
`External: false` when `own`, else today's answer; `answerPlan` sets `External: !own`.

- [x] **Step 1: Failing tests** (`builder`): the digest is stable across runs, changes with a
  byte, a name or a new file, ignores `target/` and `.git/`, errors on a symlink; `OwnDiagnostics`
  on recorded logs (a type error in `src/logic.rs` kept with its `-->` line; a
  `warning: serde@1.0.0: …` line dropped; an `error: could not compile` line dropped; a
  `Cargo.toml` parse error kept; nothing kept → the fixed sentence).
- [x] **Step 2: Failing tests** (`tool/builtin`): a write in a clean session writes, commits with
  the exact message and records provenance; a tainted call is refused and writes nothing; `..`,
  `/abs`, `.cargo/config.toml`, `target/x`, a symlinked folder are refused; 256 KB + 1 is refused;
  the 201st file and the module over 16 MB are refused; a commit failure leaves the old file;
  `module.read` of a recorded folder is not external, after a file changed by hand it is; a build
  of a recorded folder answers with only own diagnostics and `External: false`, of a changed
  folder as today; `module.import` records `example:<from>`.
- [x] **Step 3: Run, see them fail. Implement.**
- [x] **Step 4: `make lint test`; code map rows (`builder`, module tools, the provenance folder);
  commit, push.**

```bash
git commit -m "tool/builtin: the agent writes and reads module source; a build of its own clean source does not taint (R-RUN-17)"
```

---

### Task 7: The reflex template and the module-writing skill

**Files:**
- Create: `examples/modules/reflex-template/{Cargo.toml,Cargo.lock,.gitignore,manifest.yaml,README.md}`,
  `examples/modules/reflex-template/src/{lib.rs,host.rs,guest.rs,fake.rs,logic.rs}`,
  `skills/builtin/module-writing/SKILL.md`
- Modify: `.github/workflows/ci.yml` (the template's fmt, clippy, test, wasm build),
  `docs/architecture/05-code-map.md`

**Interfaces:**
- Produces: the template's seams, copied from `deye-reader` without the HTTP parts:

```rust
// host.rs
pub trait Host {
    fn state_get(&self, key: &str) -> Option<String>;
    fn state_put(&self, key: &str, value: &str);
    fn config(&self, key: &str) -> Option<String>;
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)>; // value, ms changed
    fn notify(&self, text: &str) -> Result<(), Notify>;              // Notify::{Denied, Limit, NotHere}
    fn now_unix(&self) -> i64;
    fn log(&self, msg: &str);
}

// logic.rs: the one place the agent changes, with the manifest.
pub struct Input { pub trigger: String, pub cells: Vec<String> }
pub struct Reading { pub id: String, pub value: serde_json::Value, pub at_ms: i64 }
/// react decides what to say about the readings; it keeps what it must remember in state.
pub fn react(input: &Input, readings: &[Reading], state: &mut State, now_ms: i64) -> Vec<String>
```

`lib.rs` reads the input, gets every listened cell, loads `State` (serde JSON under the key
`state`), calls `react`, stores the state, sends each message with `notify`, and returns
`{"ok":true}`; a `Limit` stops the remaining messages for this run. The template's own rule is a
neutral example: one listened cell `producer.value`, a message when it crosses a configured
`threshold` downwards, once until it rises again, and "no data for <n> min" once from the hourly
cron when the cell is older than `stale_after` minutes (default 60). Its tests cover each rule on
the fake host.

Manifest:

```yaml
name: reflex-template
version: 1
abi: 1
triggers:
  - cell: [producer.value]
  - cron: "15 * * * *"        # the staleness check
capabilities:
  state: true
  notify: {per_day: 24}
cells:
  read: [producer.value]
config:
  threshold:   {type: number, default: "30"}
  stale_after: {type: number, default: "60"}
limits:
  memory: 16MiB
  timeout: 5s
escalate:
  - on: module.exception
```

The skill:

```markdown
---
name: module-writing
description: Write, build and offer a module of the owner's system, such as a reflex that reacts to cells and writes to the owner
---
The system you are changing is what you run on as an agent. Be careful not to break it: if it
breaks, you stop working.

A module does its one task fully and hands the system contract data. …
```

The body states, in this order: the two mandatory instructions of spec 9.4 word for word; when
to make a module (a reaction that repeats) and when not; one module, one responsibility, data
in and out only through cells; the loop (`module.import` of `reflex-template` under the new
name → `module.read` → `module.write` of `manifest.yaml` and `src/logic.rs` only → `module.build`
→ read the diagnostics → fix → `module.install`); message text from a template in code, never a
model, never text from outside; least rights (no network or secrets a reflex does not need,
`per_day` sized to the rules); a Rust test for every rule; never listen to the module's own
cells; dependencies only from the template's `Cargo.lock`; `module.write` works only in a clean
conversation, so start a new one when it refuses.

- [ ] **Step 1: Write the template's Rust tests first** (`cargo test --locked` fails), then the
  code; `cargo clippy --locked --target wasm32-wasip1 -- -D warnings` clean.
- [ ] **Step 2: A Go test** builds the template with the node's builder (skips without Rust),
  installs it in a `modhost` test with a probe producer, and sees one `module.notify` event when
  the producer reports a value below the threshold.
- [ ] **Step 3: The skill**; `skills` tests load it (name, description, no script).
- [ ] **Step 4: CI step for the template; `make lint test`; commit, push.**

```bash
git commit -m "examples: the reflex template; skills: module-writing"
```

---

### Task 8: Acceptance and closing (design section 8)

**Files:**
- Create: `tools/chatlab/scenarios/m4c-cell-trigger-notify.yaml`, `m4c-notify-limit.yaml`,
  `m4c-loop-refused.yaml`, `m4c-write-tainted.yaml`, `m4c-write-build-template.yaml`;
  `tools/chatlab/scenarios/modules/{producer,listener,listener-loop}.yaml`
- Modify: `docs/plans/p1-all-in-one.md`, `docs/README.md`, this plan's checkboxes; the board

| # | Check | Proof |
|---|---|---|
| 1 | A producer's cron run starts its listener, which writes to the owner, with no model turn | `m4c-cell-trigger-notify` (two probes, `advance: {scheduler}`, a reply containing the probe's text, `history` with the note, no provider call) |
| 2 | The daily limit holds and the owner gets one limit line | `m4c-notify-limit` |
| 3 | A loop is refused at install | `m4c-loop-refused` (`module.install` refused, "a → b → a") |
| 4 | `module.write` is refused after outside content | `m4c-write-tainted` (a fetched page, then `module.write` refused; no file) |
| 5 | Import, write, build, fix, window | `m4c-write-build-template` (scripted; builds with Rust, so `GANGLION_NET_TESTS`) |
| 6 | An event survives a restart; a repeated run sends nothing twice | `modhost` and `agent` tests of tasks 3 and 4 |
| 7 | Live: the owner's one message → the agent writes `deye-alerts`, the window, the press, alerts at :00/:30 with no model turn; cards show the chain | the owner, on the test bot |

- [ ] **Steps:** write the scenarios; `make lab` for them; `make test-net` for check 5; `make
  memory` (idle within M4b's numbers; a growth needs a reason); the live check with the owner
  (the owner's message is the design's section 8 text); fixes from the live check as task 8b;
  "Where we are" closes M4c; the board's M4c cards done; merge `m4c-reflexes` into `master` with
  the owner's go.

```bash
git commit -m "docs: M4c closed — reflex modules: cell triggers, notify, the agent writes deye-alerts"
```
