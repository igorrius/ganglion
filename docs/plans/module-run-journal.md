# Module run journal — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** record every run of a module (its cause, time, duration,
outcome, host calls by kind, cells written, messages sent) in a bounded
journal, link a module's message to its run, and show the chain that led
to a message in the chat with no model: `/why` as a reply, a Runs button
on the card, `/modules runs <name>`, `module.status` and the reply note
([#169](https://github.com/igorrius/ganglion/issues/169)).

**Architecture:** `modstore` gets a `module_runs` table and its reads and
writes; `runner` reports each host call's kind to an observer;
`modhost` counts the calls on the run, times it, writes the row when the
run ends (copying the producer's chain for a cell trigger), stamps the
run id on a module's message and adds the last runs to the card; `event`
and `agent` carry the run id in the message's origin and render the chain
for `/why`, `/modules runs`, the Runs button and the reply note;
`tool/builtin` adds the last runs to `module.status`.

Decision: [ADR-0091](../adr/0091-a-bounded-run-journal-per-module-with-the-chain-copied-in.md),
spec R-RUN-20 (and R-RUN-16, R-DLV-15). Read
[conventions](../conventions.md) before writing code.

## Global constraints

- All code, comments, docs and the texts' English source in English; the
  `uk` and `ru` texts read naturally and match their neighbours.
- **Never store or show a module's text:** no notify text, no cell value,
  no argument, no module error. Outcome and kind are the node's closed
  sets (`modhost.Result`, `modhost.Kind`). A cell id or a module name is
  shown (to the owner or the model) only when it matches the node's name
  pattern (`plainName` in `tool/builtin/module_status.go`:
  `^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)*$`, at most 253 bytes); move that
  check to one exported place (for example `modstore.PlainName(s) bool`)
  and use it everywhere this plan shows a name. A name that fails it is
  shown as `?`.
- Recording never fails or delays a run's outcome: a failed write is
  logged (`Warn`) and counted in a metric.
- A trial run (`modtrial`, `modhost/trial.go`) records nothing and sets no
  observer.
- Every new text key goes to `texts/texts.go` (constant and the key list)
  and to `texts/lang/en.yaml`, `uk.yaml`, `ru.yaml`.
- Before each commit: `go build ./... && go vet ./... && go test ./...`;
  `golangci-lint run` when installed. Commit after each task with a
  message `<area>: <what>` and the trailer
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

---

### Task 1: The runs table in modstore

**Files:**
- Create: `modstore/migrations/006_runs.sql`, `modstore/runs.go`
- Modify: `modstore/local.go` (`RemoveLocal` deletes the module's runs)
- Test: `modstore/runs_test.go`

**Interfaces (produces):**

```go
package modstore

// RunsKept is how many runs of a module the journal keeps (R-RUN-20).
const RunsKept = 200

// Hop is one run as a chain shows it: no ids beyond the module's name.
type Hop struct {
	Module  string         `json:"module"`            // full name, "local/demo-reader"
	Version string         `json:"version"`
	Trigger string         `json:"trigger"`           // cron | tool | cell
	Tool    string         `json:"tool,omitempty"`    // the tool's name, "demo-reader.status", for tool
	Cells   []string       `json:"cells,omitempty"`   // the cells whose change started it, for cell
	At      time.Time      `json:"at"`
	Took    time.Duration  `json:"took"`
	Result  string         `json:"result"`            // modhost.Result
	Kind    string         `json:"kind,omitempty"`    // modhost.Kind when it failed
	Calls   map[string]int `json:"calls,omitempty"`   // host call kind → count
	Wrote   []string       `json:"wrote,omitempty"`   // cell ids reported, sorted
	Sent    int            `json:"sent,omitempty"`    // messages notify put
}

// Run is one row of the journal: the run, its id, who produced it and the
// chain of runs that led to it, oldest first (at most 4).
type Run struct {
	Hop
	ID          string // the run's id: the cron slot, the call's key, the trigger event's id
	SHA256      string
	Producer    string // for cell: the module whose run wrote the cells
	ProducerRun string // and that run's id
	Depth       int
	Chain       []Hop
}

// RecordRun writes r, replacing a row with the same module and id, and
// prunes the module's rows beyond RunsKept (oldest by At, then rowid).
func (s *Store) RecordRun(ctx context.Context, r Run) error

// Run is the row of module's run id; false when the journal has none.
func (s *Store) Run(ctx context.Context, module, id string) (Run, bool, error)

// Runs are module's last n runs, newest first.
func (s *Store) Runs(ctx context.Context, module string, n int) ([]Run, error)
```

Table `module_runs`: `module TEXT, run_id TEXT, version, sha256,
trigger, tool, cells (JSON), producer, producer_run, depth, at_ms,
took_ms, result, kind, calls (JSON), wrote (JSON), sent, chain (JSON)`,
`PRIMARY KEY (module, run_id)`, and an index on `(module, at_ms)`.
Follow the header comment style of `003_journal.sql` (times are Unix ms
UTC).

- [x] **Step 1: Failing tests** (use the store test helpers already in
  `modstore/store_test.go`): a run written and read back with every field
  (calls, wrote, chain with two hops); the same id written twice is one
  row with the second's fields; 205 runs keep the newest 200 and `Runs(n=10)`
  is the newest 10 newest first; another module's rows are untouched by
  the prune; `RemoveLocal` drops the module's runs; `Run` of an unknown id
  is `false, nil`.
- [x] **Step 2:** `go test ./modstore -run Run` — FAIL.
- [x] **Step 3: Implement.** `INSERT OR REPLACE`, then
  `DELETE FROM module_runs WHERE module = ? AND rowid NOT IN (SELECT rowid
  FROM module_runs WHERE module = ? ORDER BY at_ms DESC, rowid DESC LIMIT ?)`,
  in one transaction. JSON columns default to `'[]'`/`'{}'`/`''` as fits;
  decode empty as nil.
- [x] **Step 4:** `go test ./modstore/...` — PASS.
- [x] **Step 5:** commit `modstore: a bounded journal of module runs`.

### Task 2: The runner reports its host calls

**Files:**
- Modify: `runner/runner.go` (`Deps`, `instantiateHost`)
- Test: `runner/calls_test.go` (new; reuse `runner/runnertest` and its
  probe module; check which ops the probe has and add one only if none
  calls a host function you need)

**Interfaces (produces):**

```go
// Host call kinds reported to Deps.OnCall: a closed set.
const (
	CallHTTPFetch  = "http.fetch"
	CallCellGet    = "cell.get"
	CallCellReport = "cell.report"
	CallNotify     = "notify"
	CallStateGet   = "state.get"
	CallStatePut   = "state.put"
	CallSecretGet  = "secret.get"
	CallConfigGet  = "config.get"
)

// Deps.OnCall, when set, is told the kind of every host call a module
// makes, before the call's own checks, so a denied call counts too. ctx
// is the call's: it carries what the caller put in Handle's context. nil
// reports nothing (a trial run).
OnCall func(ctx context.Context, kind string)
```

- [x] **Step 1: Failing test:** a probe call that fetches, reads and
  reports a cell (whatever the probe offers) with an `OnCall` that
  appends kinds; assert the kinds and their counts; a value put in
  `Handle`'s ctx is visible in `OnCall`'s ctx; `log`, `now_ms` and
  `take` report nothing.
- [x] **Step 2:** FAIL.
- [x] **Step 3: Implement.** A helper `m.called(ctx, kind)`; the
  `state_get`, `state_put`, `secret_get` and `config_get` builders take
  `ctx context.Context` instead of `_`.
- [x] **Step 4:** `go test ./runner/...` — PASS.
- [x] **Step 5:** commit `runner: report each host call's kind`.

### Task 3: modhost records each run

**Files:**
- Modify: `modhost/run.go` (the run carries its cause and a call counter),
  `modhost/tools.go` (`call` times the run; `finish` records it before
  `fanOut`; `runTool` passes the tool's name), `modhost/trigger.go`
  (`RunTrigger` passes producer, producer run and cells),
  `modhost/load.go` (`runner.Deps.OnCall` counts on the ctx's run),
  `modhost/host.go` (`Store` port: `RecordRun`, `Run`, `Runs`),
  `modhost/notify.go` (the message carries `Run: r.ID`),
  `modhost/admin.go` (`Card` fills `Runs`; `Host.Run(ctx, name, id)`),
  `modhost/telemetry.go` (counter), `modstore/card.go` (`Card.Runs`),
  `event/message.go` (`ModuleNotify.Run`)
- Test: `modhost/journal_test.go` (new), and fakes in the existing tests
  for the grown `Store` port

**Behaviour:**
- `run` gets `tool string`, `producer, producerRun string`,
  `cells []string` and `calls *counts` (a mutex-guarded `map[string]int`,
  like `written`). `newRun` keeps its signature for cron; add what the
  tool and trigger paths need in the least awkward way.
- `ran` gets `at time.Time` (`h.d.Now()` before `Handle`) and `took`.
- `finish` calls `h.record(ctx, r, o)` after `settle`, before `fanOut`.
  `record` builds a `modstore.Run` (Hop from `l.name`, `l.version`,
  `l.sha`, the trigger, the cause, `o.result`, `o.kind` when failed,
  `calls`, `wrote.list()`, `*sent`), and for a cell trigger reads the
  producer's row with `Store.Run(producer, producerRun)`: when found,
  `Chain = append(producer.Chain, producer.Hop)` cut to the last
  `maxDepth`; when not, the chain is empty and the row still names the
  producer. Failures to read or write are logged and counted in
  `ganglion.modhost.journal.failures` (by module); the run goes on.
- `init` and `health` record nothing (they carry no run).
- The trial path records nothing: check `modhost/trial.go` and keep it so.
- `notifyPort.notify` sets `event.ModuleNotify.Run = r.ID` for the module's
  text; node lines (limit, chain stopped) carry none.
- `Card` fills `Runs` with `Store.Runs(name, 10)`; a failed read is
  logged and leaves `Runs` empty (the card still shows).
- `Host.Run(ctx, name, id) (modstore.Run, bool, error)` accepts the name
  with or without `local/`, like the other `LocalModules` methods.

- [x] **Step 1: Failing tests** with the host test helpers in
  `modhost/host_test.go`/`trigger_test.go`: a cron run that reports a
  cell records a row (trigger cron, result ok, `cell.report` 1, wrote the
  cell, took ≥ 0); a tool call records trigger tool and the tool's name;
  a failed run records its result and kind; a cell-triggered run of a
  listener after a producer's cron run records producer, producer run,
  cells, depth 1 and a chain of one hop equal to the producer's row; a
  third module down the chain has a chain of two hops; a notify run's
  `module.notify` event carries the run id; a store whose `RecordRun`
  fails still settles the run and fans out; `Card` has the last runs.
- [x] **Step 2:** FAIL.
- [x] **Step 3: Implement.**
- [x] **Step 4:** `go test ./modhost/... ./modtrial/...` — PASS.
- [x] **Step 5:** commit `modhost: record each run of a module and the chain that started it`.

### Task 4: A module's message names its run

**Files:**
- Modify: `event/origin.go` (`Origin.Run string \`json:"run,omitempty"\``,
  for `OriginModule` only), `agent/module_notify.go` (the outgoing
  message's origin carries `p.Run`)
- Test: `agent/module_notify_test.go`

- [ ] **Step 1: Failing test:** a module's text with `Run: "r1"` stores
  an outgoing message whose origin has `Run == "r1"`; a node line has none.
- [ ] **Step 2–4:** implement; `go test ./event/... ./agent/...` — PASS.
- [ ] **Step 5:** commit `agent: a module's message records the run that sent it`.

### Task 5: The owner sees runs and chains

**Files:**
- Create: `agent/runs.go` (rendering and `/why`), `agent/runs_test.go`
- Modify: `agent/modules.go` (`LocalModules` gets `Run`; `NoLocalModules`
  answers `false`; `/modules runs <name>`; the card's Runs button),
  `agent/commands.go` (`/why` in the table, with hints in three languages;
  the `/modules` hint names `runs`), `agent/agent.go` (the quick action
  `modules.runs`), `texts/texts.go`, `texts/lang/{en,uk,ru}.yaml`

**What it shows** (the English source; `uk` and `ru` match it):

```
How this message came about:
1. 16:30 demo-reader v3, by cron · 0.4 s · ok
   http.fetch 2, cell.report 2 · wrote demo-reader.soc, demo-reader.load
2. 16:30 demo-alerts v1, on a change of demo-reader.soc · 0.0 s · ok
   cell.get 3, notify 1
→ this message
```

and for `/modules runs demo-alerts` (and the Runs button):

```
Last runs of demo-alerts:
16:30 v1, on a change of demo-reader.soc by demo-reader · 0.0 s · ok
   cell.get 3, notify 1
16:00 v1, by the agent's call of demo-alerts.status · 0.1 s · failed: stale
   cell.get 1
```

- Names are short (`moduleName` strips `local/`), escaped (`texts.Escape`)
  and pass the name pattern or show as `?`. Times are in
  `a.cfg.Location`, `15:04` today and `02.01 15:04` on another day.
  Durations use `seconds` from `agent/last.go`. Calls are listed in a
  fixed order (the runner's constants' order), `kind n`, comma-separated;
  the second line is left out when there are no calls and nothing was
  written. A failed run shows `failed: <kind>`.
- Keys (names are a suggestion; follow `texts` naming):
  `WhyHeader`, `WhyThisMessage`, `WhyUsage` (no reply, or a reply to
  something that is not a module's message: "Reply with /why to a module's
  message to see how it came about."), `WhyNoRun` (the message has no run:
  sent before the journal or a node line), `WhyGone` (the run is no
  longer in the journal: "it keeps the last 200 runs of each module"),
  `RunCauseCron`, `RunCauseTool` (`{tool}`), `RunCauseCell` (`{cells}`)
  and `RunCauseCellBy` (`{cells}`, `{producer}`), `RunResultOK`,
  `RunResultFailed` (`{kind}`), `RunWrote` (`{cells}`), `RunsHeader`
  (`{name}`), `RunsNone`, `ButtonRuns`.
- `/why`: admin only (`setAdmin`; not available otherwise, as
  `/modules`). It reads the replied message with `a.lookupReplied`
  (`agent/reply.go`) using the turn's transport, conversation and
  message. Not found, not the bot's or not `OriginModule` → `WhyUsage`;
  `Run == ""` → `WhyNoRun`; `Local.Run` false → `WhyGone`; else the chain
  (each hop of `Chain`, then the run's own hop) and `WhyThisMessage`.
  The history gets a `[command]` note in the style of `/modules`, naming
  the module and nothing else. Commands are not turns (R-OBS-10).
- The card: when the module has runs, a Runs button stored like the
  History button of `/cells` (`historyButtons` in `agent/cells.go`):
  action `modules.runs`, args `{"module": "<short name>"}`, the same TTL;
  its quick action answers what `/modules runs <name>` answers. Read how
  `/modules` sends its reply (`t.reply`) and how `/cells` attaches
  buttons to a message, and attach the button to the card's message the
  same way.

- [ ] **Step 1: Failing tests** in `agent/runs_test.go` with the agent
  test helpers (see `agent/modules_test.go`, `agent/last_test.go`,
  `agent/cells_test.go` for the History button): `/why` replying to a
  module's message with a two-hop chain shows the text above; to a
  message with no run → `WhyNoRun`; to a run not in the journal →
  `WhyGone`; without a reply or to a turn's message → `WhyUsage`; from a
  non-admin → not available; a cell id that fails the pattern shows `?`;
  `/modules runs demo-alerts` lists the card's runs; the card carries a
  Runs button and pressing it shows the same list; no rendered text holds
  a module's notify text.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** `go test ./agent/... ./texts/...` — PASS (the texts
  tests check that every key exists in each language).
- [ ] **Step 5:** commit `agent: /why, /modules runs and the Runs button show a module's runs`.

### Task 6: The agent sees runs

**Files:**
- Modify: `agent/reply.go` (the module note gains the chain),
  `tool/builtin/module_status.go` (`last_runs`), and their tests
  (`agent/reply_test.go`, `tool/builtin/module_status_test.go` and its
  golden file)

**Behaviour:**
- Reply note: where the reply's row is a module's message whose origin
  has a run, the caller of `replyNoteFor` (`agent/reply.go` around line
  233) reads it with `a.d.Local.Run`; a failed or missing read leaves the
  note as it is now. The note becomes
  `[in reply to the message the module demo-alerts sent at 16:31; how it came about: demo-reader v3 ran at 16:30 by cron, called http.fetch 2, cell.report 2 and wrote demo-reader.soc, demo-reader.load; then demo-alerts v1 ran at 16:30 on a change of demo-reader.soc and called cell.get 3, notify 1` + `moduleWrote` + `]`.
  English node words only; names through the name pattern (`?`
  otherwise). The note's kind stays `replyModule`: it does not taint.
- `module.status`: `last_runs`, up to 5 newest first, each `{at
  (RFC3339), trigger, tool?, producer?, cells?, took_ms, result, kind?,
  calls, wrote?, sent?}`; names through `plainCells`/the shared pattern;
  the answer's outside-content rule is unchanged (no string here is the
  module's text).

- [ ] **Step 1: Failing tests:** the reply note with a run in the journal
  carries the chain and is not outside; with a run missing it is today's
  note; `module.status` golden with two runs.
- [ ] **Step 2–4:** implement; `go test ./agent/... ./tool/...` — PASS.
- [ ] **Step 5:** commit `agent, module.status: the agent sees a module's runs and a message's chain`.

### Task 7: Docs

**Files:**
- Modify: `docs/guide/using/modules.md` (a section "Why did a module
  write?": `/why` as a reply, the Runs button and `/modules runs <name>`,
  the 200-run bound; add `runs` to "The `/modules` verbs"),
  `docs/guide/using/commands.md` (a `/why` row: admin, admin chat),
  `docs/architecture/05-code-map.md` (modstore's runs, runner's call
  observer, modhost's journal, agent's `/why`),
  `docs/README.md` ("Where we are": one sentence with the issue link),
  this plan (tick the steps).

- [ ] **Step 1:** write; placeholders only, no real names; the guide has
  no spec ids or ADR talk.
- [ ] **Step 2:** if `zensical` or `mkdocs` is installed, build the site
  strictly as the `Docs` workflow does (see `.github/workflows/`);
  otherwise check every new link by hand.
- [ ] **Step 3:** commit `docs: the module run journal`.
