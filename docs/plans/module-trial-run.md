# Trial run of a module — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** after `module.build`, run the real `.wasm` on the module's
`examples.yaml` in a sandbox, tell the model the outcome in node words,
show the owner what the module would send in the install window, and
refuse the install when the trial fails ([#167](https://github.com/igorrius/ganglion/issues/167)).

**Architecture:** a clock seam in `runner`; a new package `modtrial` that
parses examples and runs a build on them with in-memory ports; `modhost`
adds the run on the node's current cells; the build job in
`tool/builtin` runs the trial, stores the report beside the build, and the
plan and the install window read it.

**Tech stack:** Go, wazero (`runner`), `gopkg.in/yaml.v3` strict decoding
as `runner.ParseManifest` does, the test module `runner/runnertest/probe`.

Design: [module-trial-run-design.md](module-trial-run-design.md). Decision:
[ADR-0090](../adr/0090-a-module-is-tried-on-its-examples-before-install.md),
spec R-RUN-19. Read [conventions](../conventions.md) before writing code.

## Global constraints

- All code, comments, docs and texts' English source in English; `uk` and
  `ru` texts read naturally and match their neighbours.
- The model never reads a module's text: no notify text, no reported
  value, no module error text in anything that reaches a `tool.Result`.
  Example names are source text: the build answer is outside content
  unless the source is the node's own (existing `own` flag).
- Nothing of the node changes during a trial: no cell write, no message,
  no `module_state` row, no count, no escalation, no metric of
  `runner`'s instruments, no real secret read, no network dial.
- Limits: `examples.yaml` ≤ 32 KiB; ≤ 10 examples; ≤ 20 steps per example;
  HTTP body ≤ 64 KiB; whole trial ≤ 60 s; each call the manifest's timeout
  and memory; the window's trial section ≤ 1200 runes.
- Every new text key goes to `texts/texts.go` (constant and the key list)
  and to `texts/lang/en.yaml`, `uk.yaml`, `ru.yaml`.
- Run `go build ./... && go vet ./... && go test ./...` and, where named,
  `go test -tags lab ./internal/chatlab/...`, before each commit; run
  `golangci-lint run` when it is installed. Commit after each task with a
  message `<area>: <what>` and the trailer
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

---

### Task 1: A clock seam in the runner

**Files:**
- Modify: `runner/runner.go` (`Deps`, `instantiateHost` `now_ms`, `handle`'s
  `wazero.NewModuleConfig()`, `Handle`'s `observe`)
- Test: `runner/clock_test.go` (new)

**Interfaces:**
- Produces: `runner.Deps.Clock func() time.Time` (nil = `time.Now`) and
  `runner.Deps.Quiet bool` (true = `Handle` records no metrics).

- [x] **Step 1: Failing test.** With the probe module (see how
  `runner/runnertest` loads it; reuse its loader), `Deps{Clock: func()
  time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }}`, call
  `Handle` with payload `{"op":"now"}` and assert the result's `unix` is
  that time's `Unix()`. A second test: `Clock` that returns a time 10 min
  later on the second call; assert the second result moved by 600.
- [x] **Step 2:** run `go test ./runner -run Clock` — FAIL.
- [x] **Step 3: Implement.**
  - `Deps.Clock`; in `load`, `if deps.Clock == nil { deps.Clock = time.Now }`.
  - `now_ms` returns `m.deps.Clock().UnixMilli()`.
  - In `handle`, replace `WithSysWalltime().WithSysNanotime()` with the
    real ones only when the clock is the real one; otherwise
    `WithWalltime(func() (int64, int32) { t := m.deps.Clock(); return t.Unix(), int32(t.Nanosecond()) }, sys.ClockResolution(1))`
    and `WithNanotime(func() int64 { return m.deps.Clock().UnixNano() }, sys.ClockResolution(1))`
    (`github.com/tetratelabs/wazero/sys`). Keep a `clockSet bool` in
    `Module` decided in `load` (`deps.Clock != nil` before defaulting).
  - `Deps.Quiet`: `Handle` skips `m.observe` when set.
  - Doc comments on both fields say what they are for (a trial run).
- [x] **Step 4:** `go test ./runner/...` — PASS.
- [x] **Step 5:** commit `runner: an injected clock and a quiet mode for trial runs`.

### Task 2: The examples file

**Files:**
- Create: `modtrial/examples.go`, `modtrial/doc.go` (package doc)
- Test: `modtrial/examples_test.go`

**Interfaces:**
- Consumes: `runner.Manifest`, `runner.ParseManifest`.
- Produces:

```go
package modtrial

// FileName is the examples file beside a module's manifest.
const FileName = "examples.yaml"

// Limits of an examples file.
const (
	MaxFileBytes = 32 << 10
	MaxExamples  = 10
	MaxSteps     = 20
	MaxBodyBytes = 64 << 10
)

// ErrExamples wraps every reason an examples file is refused; the text
// after it is the node's words and may quote names from the file.
var ErrExamples = errors.New("examples.yaml")

type Examples struct {
	Examples []Example `yaml:"examples"`
}

type Example struct {
	Name    string            `yaml:"name"`
	Config  map[string]string `yaml:"config"`  // over the manifest's defaults; keys the manifest declares
	Secrets map[string]string `yaml:"secrets"` // fake values; names in secret.get
	HTTP    []Canned          `yaml:"http"`
	Steps   []Step            `yaml:"steps"`
}

type Canned struct {
	URL     string            `yaml:"url"` // exact; its host is in http.fetch
	Status  int               `yaml:"status"`
	Headers map[string]string `yaml:"headers"`
	Body    string            `yaml:"body"`
}

type Step struct {
	After   time.Duration          `yaml:"-"` // parsed from "after" (a Go duration, ≥ 0)
	Cells   map[string]json.RawMessage `yaml:"-"` // parsed from "cells": id → JSON value
	Trigger Trigger                `yaml:"-"`
	Expect  Expect                 `yaml:"expect"`
}

// Trigger is "cell", "cron", a tool call, or none ("" Kind).
type Trigger struct {
	Kind string          // "cell" | "cron" | "tool" | ""
	Tool string          // the manifest's tool trigger name, for Kind "tool"
	Args json.RawMessage // an object, for Kind "tool" ({} when none)
}

type Expect struct {
	Messages *int     `yaml:"messages"` // exact count of accepted notify calls
	Reports  []string `yaml:"reports"`  // exact set of cell ids reported; nil = not checked
	Fails    bool     `yaml:"fails"`    // the step must end in an error
}

// Parse reads and checks an examples file against the module's manifest.
// prefix is the module's short name: its own cells are <prefix>.<name>.
func Parse(data []byte, m runner.Manifest, prefix string) (Examples, error)
```

- [x] **Step 1: Failing tests,** table-driven, with a manifest built by
  `runner.ParseManifest` from inline YAML that has a cell trigger on
  `src.soc`, `cells.read: [src.soc]`, `cells.report: {level: …}`,
  a cron trigger, a tool trigger `status`, `http.fetch: [api.example.com]`,
  `secret.get: [token]`, config key `threshold`. Cases:
  1. the design note's example file parses; `after: 10m` → `10*time.Minute`;
     `cells` values come out as JSON (`55` → `55`, `"x"` → `"x"`,
     `{a: 1}` → `{"a":1}`); `trigger: {tool: status, args: {}}` → Kind
     "tool".
  2. refused (each `errors.Is(err, ErrExamples)` and the message names
     the problem): unknown field; file over `MaxFileBytes`; 0 examples;
     11 examples; 21 steps; empty or duplicate example name; a cell not in
     `cells.read` and not `<prefix>.<report name>`; `trigger: cell` in a
     manifest with no cell trigger; `trigger: cron` without a cron
     trigger; an unknown tool; tool `args` not an object; negative
     `after`; a URL whose host is not in `http.fetch`; a non-https URL
     when the runner's fetch requires https (check `runner`'s fetch rule
     and mirror it); body over `MaxBodyBytes`; status outside 100..599; a
     secret not in `secret.get`; a config key the manifest does not
     declare; `expect.reports` naming a cell the module does not report.
- [x] **Step 2:** `go test ./modtrial` — FAIL.
- [x] **Step 3: Implement** `Parse`: decode with `yaml.NewDecoder` and
  `KnownFields(true)` into raw structs whose `after`, `cells` and
  `trigger` are `yaml.Node`s, then convert (a cell value node →
  `json.Marshal` of its decoded `any`; a trigger scalar "cell"/"cron" or
  a mapping with `tool` and optional `args`). Messages are short and in
  the node's words, e.g. `examples.yaml: example "x" step 2: cell y.z is not in cells.read`.
- [x] **Step 4:** `go test ./modtrial` — PASS.
- [x] **Step 5:** commit `modtrial: the examples file of a module`.

### Task 3: The sandbox run

**Files:**
- Create: `modtrial/run.go` (Run, Report), `modtrial/ports.go` (cells,
  notify, HTTP transport), `modtrial/report.go` (`ModelLine`)
- Test: `modtrial/run_test.go`

**Interfaces:**
- Consumes: Task 1 (`Deps.Clock`, `Deps.Quiet`), Task 2 (`Examples`).
- Produces:

```go
// Budget is the time a whole trial may take.
const Budget = 60 * time.Second

type Report struct {
	Examples []ExampleResult `json:"examples"`
	Now      *StepResult     `json:"now,omitempty"` // the run on the node's current cells; shown, never judged
	Passed   bool            `json:"passed"`
	Refused  string          `json:"refused,omitempty"` // the examples file's refusal, node words
}

type ExampleResult struct {
	Name   string       `json:"name"`
	Passed bool         `json:"passed"`
	Steps  []StepResult `json:"steps"`
}

type StepResult struct {
	N        int        `json:"n"` // 1-based
	Ran      bool       `json:"ran"`
	OK       bool       `json:"ok"`
	Kind     string     `json:"kind,omitempty"`     // failure kind: module.trap, module.timeout, module.format, a module's kind
	Messages []string   `json:"messages,omitempty"` // accepted notify texts: the owner's eyes only
	Reports  []Reported `json:"reports,omitempty"`
	Mismatch string     `json:"mismatch,omitempty"` // node words: "expected 1 message, got 0"
}

type Reported struct {
	Cell  string          `json:"cell"`
	Value json.RawMessage `json:"value"`
}

// Options are what the host adds to a run.
type Options struct {
	Start time.Time // virtual time at each example's start
	Log   *slog.Logger
	// Current, when set, seeds a run on the node's current cells: their ids
	// and values, read by the host. Nil skips that run.
	Current map[string]Seeded
}

type Seeded struct {
	Value json.RawMessage
	At    time.Time
}

// Run runs wasm with manifest m on ex. A load failure is an error; every
// failure of the module is in the report.
func Run(ctx context.Context, wasm []byte, m runner.Manifest, prefix string, ex Examples, o Options) (Report, error)

// ModelLine is the report in the node's words, for the model: counts,
// example names, step numbers, mismatches and failure kinds; never a
// message text or a reported value.
func (r Report) ModelLine() string
```

- [x] **Step 1: Failing tests** on the probe module (built as
  `runner/runnertest` does) with a manifest granting what each test
  needs; scripts go through example `config` keys `op_cron`, `op_cell`,
  `op_tool_<name>` (the probe reads them, see `probe.scripted`):
  1. a cron step with `op_cron: {"op":"notify","value":"hi"}` and
     `expect: {messages: 1}` passes; `Messages == ["hi"]`.
  2. the same with `expect: {messages: 0}` fails with `Mismatch`
     "expected 0 messages, got 1"; `Passed` false.
  3. `per_day: 1`: two notify steps the same virtual day → the second
     notify answers `NotifyLimit` and is not counted; with `after: 24h`
     between them both count.
  4. `cell_get` of a seeded cell returns its value and `at_ms` equal to
     the virtual time when the step set it; `after: 10m` then `op: now`
     returns start + 10 min.
  5. `cell_report` of an own cell is in `Reports` with its value and is
     read back by a later step's `cell_get`; a value of the wrong type
     ends the step with kind `module.format`.
  6. `fetch` of a canned URL returns its status and body; another URL
     returns a fetch error; nothing dials (use a URL on an unroutable
     host and assert no `net` error type reaches the module beyond the
     transport's refusal).
  7. `secret` returns the example's fake value; a real secret function is
     never called (Run takes none).
  8. `spin` hits the manifest timeout: kind `module.timeout`; with
     `expect: {fails: true}` the step passes.
  9. the budget: a ctx with a 1 s deadline and spinning steps → remaining
     steps `Ran: false`, `Passed` false.
  10. each example starts with an empty state: `state_put` in example 1 is
      not seen by `state_get` in example 2.
  11. `Current` set: one extra run with trigger "cell" over the seeded
      ids; its result is in `Now`; its failure leaves `Passed` true.
  12. `ModelLine` of a mixed report contains the counts, the example name
      in quotes, "step 2", the mismatch and the kind, and none of the
      message texts or values (assert with `strings.Contains`).
- [x] **Step 2:** `go test ./modtrial` — FAIL.
- [x] **Step 3: Implement.**
  - Per example: a virtual clock (`now := o.Start`; `after` adds), a
    `runner.Load` with `Deps{Clock: func() time.Time { return now }, Quiet:
    true, State: runner.NewMemState(), Cells: cells, Notify: notify, HTTP:
    &http.Client{Transport: canned}, Secrets: fake, Prefix: prefix,
    Logger: o.Log}` and `m.ConfigValues` = the manifest's defaults over-
    laid by the example's `config`. Close the module after the example.
  - Cells port: a map of id → (value, at); `Get` of an unknown id is an
    error as a missing cell is on a node; `Report` stores with `at = now`
    and appends to the step's `Reports` (the runner checks the type and
    ownership before calling the port).
  - Notify port: counts per virtual day (`now.Format(time.DateOnly)`);
    over `per_day` → `runner.NotifyLimit`; otherwise append the text to
    the step's messages and return `runner.NotifySent`.
  - Canned transport: an `http.RoundTripper` that answers exact URL
    matches with status, headers, body; anything else returns an error
    ("not in the examples"); it never dials.
  - A step: advance time, set cells (at = now), fire the trigger:
    `pdk.Input{Trigger: "cron"}`, `{Trigger: "tool:<name>", Payload:
    args}`, `{Trigger: "cell", Payload: {"cells": [changed ids that the
    cell trigger lists], "producer": "trial"}}`. A step with no trigger
    only sets. An error from `Handle` or `Output.OK == false` is a failed
    step with its kind (mirror how `modhost` maps errors to kinds:
    `runner.ErrTimeout` → `module.timeout`, a trap → `module.trap`, else
    `Output.Kind`). Then compare `expect`.
  - The whole run under `context.WithTimeout(ctx, Budget)`; steps not run
    are recorded `Ran: false`.
  - `Passed` = every example passed and `Refused == ""`.
- [x] **Step 4:** `go test ./modtrial ./runner/...` — PASS.
- [x] **Step 5:** commit `modtrial: run a build on its examples in a sandbox`.

### Task 4: The host and the build job

**Files:**
- Create: `modhost/trial.go`; Test: `modhost/trial_test.go`
- Modify: `tool/builtin/module.go` (`ModuleHost` interface; `plan`;
  `buildPath` users), `tool/builtin/module_build.go` (`build`,
  `buildAnswer`, `answerPlan`), fakes of `ModuleHost` in `tool/builtin`
  tests and in `config`/`internal/chatlab` if any implement it
- Modify: the builds janitor if it lists extensions (grep `".json"` near
  `Builds`) so `.trial.json` is swept with its build
- Test: `tool/builtin/module_build_test.go`, `tool/builtin/module_install_test.go`

**Interfaces:**
- Consumes: Task 3 `modtrial.Run`, `Report`.
- Produces:
  - `func (h *Host) Trial(ctx context.Context, b modstore.Build, examples []byte, cfg map[string]any) (modtrial.Report, error)`
    and the same method on `tool/builtin.ModuleHost`.
  - `Modules.saveTrial(sha string, r modtrial.Report) error`,
    `Modules.loadTrial(sha string) (modtrial.Report, bool, error)`.
  - `buildAnswer.Trial string \`json:"trial,omitempty"\``.
  - Refusal text constant `trialRefusal = "the trial run failed"`.

- [x] **Step 1: Failing tests.**
  - `modhost`: `Trial` with examples parses them, runs them with the
    manifest's config values from `cfg` over defaults, and for a module
    with a cell trigger and no `http.fetch` sets `Options.Current` from
    the node's real cells of `cells.read` (a cell missing on the node is
    left out); nothing is written to the node's cells, no `module_state`
    row, no notify count (assert on the test host's stores). A refused
    examples file gives a report with `Refused` set and `Passed` false,
    not an error. No examples (`nil`) gives `Report{Passed: true}` with no
    examples and no `Now`.
  - `tool/builtin` build: a fake host whose `Trial` returns a failing
    report → the answer has `refusal: "the trial run failed"`,
    `refusal_detail` = `ModelLine()`, and `trial` = `ModelLine()`; a passing
    one → no refusal and `trial` set; no `examples.yaml` in the source →
    `trial` = "no examples.yaml: the module was not tried". The trial
    file is stored beside the build.
  - `module.install` on a build whose stored trial failed refuses before
    asking (the plan's refusal), and on one with no trial file (a build
    from before this change) plans as before.
- [x] **Step 2:** run them — FAIL.
- [x] **Step 3: Implement.**
  - `modhost/trial.go`: parse the manifest (an error → wrap
    `ErrCheckFailed` as `Check` does), `configValues`, `modtrial.Parse`
    (a refusal → `Report{Refused: err.Error()}`), `Options{Start:
    h.d.Now(), Log: <module log>, Current: …}`; read the current cells
    through `h.d.Cells.Get` (only ids in `m.Cells.Read`), JSON value and
    the cell's change time.
  - `build()`: after `saveBuild` and a first `t.m.plan(ctx, b)`, if the
    plan has no refusal: read `filepath.Join(dir, modtrial.FileName)` (a
    missing file → no trial; a file over `modtrial.MaxFileBytes` → a
    report refused with that reason; read errors are errors), call
    `t.m.Host.Trial`, `saveTrial`, then plan again.
  - `Modules.plan`: after `PlanLocal`, when the plan has no refusal and a
    stored trial for `p.SHA256` failed, set `Refusal = trialRefusal`,
    `RefusalDetail = report.ModelLine()`.
  - `answerPlan` gets the trial line (a parameter) and sets
    `buildAnswer.Trial`; `Next` on a trial refusal: "fix the module or
    its examples.yaml and build again".
- [x] **Step 4:** `go test ./modhost ./tool/... ./config/...` — PASS.
- [x] **Step 5:** commit `modhost, module.build: try a build on its examples and refuse a failed trial`.

### Task 5: The install window's trial section

**Files:**
- Modify: `tool/builtin/module_window.go` (`window`, `windowOf`, `windowText`),
  `tool/builtin/module_install.go` (`prepare` passes the stored report),
  `texts/texts.go`, `texts/lang/{en,uk,ru}.yaml`
- Test: `tool/builtin/module_window_test.go` (or the existing window tests)

**Interfaces:**
- Consumes: Task 4 `loadTrial`, Task 3 `Report`.
- Produces: `window.Trial []string` (the section's lines, computed once
  from the stored report so `sameWindow` compares them); text keys
  `AskModuleTrial` ("Trial run: {passed} of {total} examples passed"),
  `AskModuleTrialNone` ("No trial run: the module has no examples."),
  `AskModuleTrialExample` ("{name}:"), `AskModuleTrialMessage`
  ("✉️ {text}"), `AskModuleTrialCell` ("{cell} = {value}"),
  `AskModuleTrialNothing` ("nothing sent or written"), `AskModuleTrialNow`
  ("Now, on the current cells:"), `AskModuleTrialMore` ("… and {n} more").

- [x] **Step 1: Failing tests.** A window with a report of two examples
  (messages, a reported cell) and a `Now` shows, after the limits line,
  the section in order; message text with Markdown, a newline and a fake
  "✅ Approve" line is shown escaped on one line exactly as a module's
  message is in chat (reuse the helper `agent/module_notify.go` uses, or
  `shown`); a value is shown as compact JSON in a code span; a report
  whose lines pass 1200 runes ends with "… and N more" and the whole
  question stays under `tool.MaxQuestionRunes`; no trial file → the
  "No trial run" line; `sameWindow` holds between two `prepare` calls.
- [x] **Step 2:** run — FAIL.
- [x] **Step 3: Implement** `trialLines(sp, report) []string` with the
  cap, set `window.Trial` in `prepare` from `loadTrial`, and append the
  lines in `windowText` after `AskModuleLimits`. Add the keys and their
  `uk`/`ru` texts.
- [x] **Step 4:** `go test ./tool/... ./texts/... ./agent/...` — PASS.
- [x] **Step 5:** commit `module.install: the trial run in the install window`.

### Task 6: Templates, skill, lab and docs

**Files:**
- Create: `examples/modules/reflex-template/examples.yaml`,
  `examples/modules/reader-template/examples.yaml`
- Modify: the embedded templates list/test (`examples/modules/templates.go`,
  `templates_test.go`) if files are listed explicitly; the built-in skill
  `module-writing` (grep for its `SKILL.md` or embedded text); the
  `modhost/template_test.go` (net-gated) to assert the trial passes;
  `tools/chatlab/scenarios/` (a new scenario and its module files beside
  the others under `scenarios/modules/`)
- Docs: `docs/architecture/05-code-map.md` (new package `modtrial`; the
  `runner` row: `Deps.Clock`, `Deps.Quiet`); `docs/guide/using/modules.md`
  (the "Approve" section: the trial section, what blocks an install, what
  `examples.yaml` is for); `docs/plans/p1-all-in-one.md` only if #167 is
  listed there; tick this plan.

- [x] **Step 1:** write the two `examples.yaml` files so that, against
  each template's `logic.rs` and manifest, every example passes: the
  reflex — a value under the threshold sends nothing, a crossing sends
  one message, a stale reading (`after` past `stale_after`) behaves as
  `logic.rs` decides; the reader — a canned answer of the template's API
  shape reports its cells, an HTTP 500 fails as the template fails it
  (`fails: true`). Read `logic.rs`/`engine.rs` to get them right.
- [x] **Step 2:** the skill: a short section "Examples": write
  `examples.yaml` with each module, one example per behaviour the owner
  asked for, counted expectations; the build's answer carries a `trial`
  line; a failed trial refuses the install, so fix the module or the
  example and build again; never put the owner's real data or secrets in
  examples.
- [x] **Step 3:** lab scenario `m4c-trial-run.yaml` modelled on
  `m4c-write-build-template.yaml`: build the reflex template with its
  examples → the build note carries `trial` and no refusal → the install
  question has the trial section. A second scripted scenario with an
  `examples.yaml` whose expectation fails → refusal, and the model's
  history holds the step and the mismatch but not the message text.
- [x] **Step 4:** docs as listed; the guide in a runner's words, no spec
  ids or ADR talk.
- [x] **Step 5:** `go build ./... && go vet ./... && go test ./... && go test -tags lab ./internal/chatlab/...`;
  `GANGLION_NET_TESTS=1 go test ./modhost -run Template` when the network
  is available (report if not run). Commit `templates, skill, lab: examples and the trial run`.
