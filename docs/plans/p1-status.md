# P1 · `/status` in Telegram — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** `/status` answers an admin, with no model, how the node is doing now: a verdict, then the
node, the models, delivery and what is off, each line with its reason.

**Architecture:** a leaf package `status` holds the entity (`Check`), the interface every component
implements (`Source`) and `Collect`, which asks the sources at once under a timeout each. Each
component that knows a fact implements `Source` in its own package; `config` lists the sources and
hands them to the agent. The agent adds the two lines only it knows (the session's models, the day
against the limits), orders the lines by section and renders them as fixed texts in the person's
language.

**Tech stack:** Go 1.27, SQLite through the existing packages. No new dependency.

**Spec:** [p1-status-design.md](p1-status-design.md), approved by the owner on 2026-11-08; R-OBS-11.
Read it with this plan.

## Global Constraints

- All documentation, comments and doc comments are in English; [conventions](../conventions.md) apply.
- Architecture docs change in the same commit as the code they describe (`05-code-map.md`, its
  dependency diagram included).
- `status` imports nothing of ganglion but `texts` and `internal/clock`. Domain packages never import `samber/do`.
- A reason is a `texts` key with values from closed sets, the configuration, numbers and times: never
  an error's text. A provider error kind (`turns.Attempt.Error`) is a closed set and may be shown.
- Every new `texts` key has its English text in `texts/lang/en.yaml` and its Russian and Ukrainian ones
  in `ru.yaml` and `uk.yaml`, with the same placeholders (`TestShippedFilesMatchEnglish`). No markup
  marks (`*_` `` ` `` `~[]<>#|`) in any template.
- Time comes from an injected clock; no `time.Now` in logic, no `time.Sleep` in tests. Run the touched
  packages with `-race`.
- A full chatlab run is never automatic: `make lab` for what changed; ask the owner before `make lab-all`.
- Commit only on branch `status`, never on `master`.

## Review Focus

1. **A source that hangs or panics** costs one line, never the command (task 1).
2. **A value with a markup mark** (a skill named `my_skill`) shows as written, not in italics (task 2).
3. **`off` never raises the verdict;** `warn` and `fail` do, and the count is theirs (task 1).
4. **A non-admin** learns nothing, and `/status` leaves no note in the history (task 2).
5. **A quiet chat is not a warning:** Telegram's line counts polls, not messages (task 5).
6. **Skills' reasons** come from a closed set of kinds, never the loader's error text (task 7).

## Files

| File | Responsibility |
|---|---|
| `status/status.go` (new) | `Section`, `State`, `Check`, `Scope`, `Source`, `Collect`, `Order`, `Verdict` |
| `status/format.go` (new) | `Bytes`, `Duration`, `Clock`: values formatted the same everywhere |
| `status/proc/proc.go` (new) | the node's own lines: version, uptime, memory, file sizes |
| `texts/texts.go`, `texts/lang/*.yaml` | the `status.*` keys; `Escape` exported |
| `agent/status.go` (new) | `/status`: admin check, the agent's own lines, rendering |
| `agent/commands.go`, `agent/agent.go` | the command row and `Deps.StatusSources` |
| `turns/status.go` (new) | models' last answer and failure; failed turns |
| `transport/telegram/health.go` (new) | the last poll, a stop by `409` or `401` |
| `queue/sqlqueue/status.go` (new) | inbox and outbox waiting, the oldest, undelivered |
| `skills/status.go` (new), `skills/skills.go` | problem kinds; skills not loaded |
| `modstore/status.go` (new), `modstore/store.go` | modules fallen back at start |
| `schedule/status.go` (new), `schedule/schedule.go` | jobs, the next run, lag, a failing tick |
| `config/status.go` (new), `config/config.go`, `config/agent.go` | the list of sources |
| `tools/chatlab/scenarios/mech-status.yaml` (new), a fault patch | the lab's check |

---

### Task 1: The `status` package

**Files:** Create `status/status.go`, `status/format.go`, `status/status_test.go`, `status/format_test.go`.
Modify `docs/architecture/05-code-map.md` (the `status` row: planned → done (task 1); a `status` node in
the dependency diagram with `status --> texts`).

**Interfaces:**
- Produces:
  ```go
  type Section string // SectionNode, SectionModels, SectionDelivery, SectionProblems
  type State string   // OK, Warn, Off, Fail
  type Check struct {
      Section Section
      State   State
      Line    texts.Key  // the line's text
      Reason  texts.Key  // "" when there is none
      Vars    texts.Vars // the values of Line and Reason, already formatted
      Since   time.Time  // when the state began; zero when unknown
  }
  type Scope struct {
      Conv, Principal string
      Now             time.Time
      Location        *time.Location
  }
  type Source interface {
      Name() string // a closed-set name for the "no answer" line: "telegram", "turns", ...
      Status(ctx context.Context, s Scope) []Check
  }
  func Collect(ctx context.Context, s Scope, sources []Source, timeout time.Duration) []Check
  func Order(checks []Check) []Check // stable, by section
  func Verdict(checks []Check) (State, int) // OK, Warn or Fail; the count of warn and fail lines
  func Bytes(n int64) string             // "512 B", "45 MB", "1.2 GB"
  func Duration(d time.Duration) string  // "40 s", "12 min", "3 h 5 min", "3 d 4 h"
  func Clock(t time.Time, s Scope) string // "12:40" today in s.Location, else "2026-11-07 12:40"
  ```
- The "no answer" line uses keys `texts.StatusNoAnswer` (`{component}: no answer`) added in this task,
  with its three translations.

- [x] **Step 1: Write the failing tests.**

  ```go
  package status_test

  type fixed struct {
  	name   string
  	checks []status.Check
  }

  func (f fixed) Name() string                                        { return f.name }
  func (f fixed) Status(context.Context, status.Scope) []status.Check { return f.checks }

  type blocking struct{}

  func (blocking) Name() string { return "slow" }
  func (blocking) Status(ctx context.Context, _ status.Scope) []status.Check {
  	<-ctx.Done()
  	return []status.Check{{Section: status.SectionNode, State: status.OK, Line: "late"}}
  }

  type panicking struct{}

  func (panicking) Name() string                                        { return "broken" }
  func (panicking) Status(context.Context, status.Scope) []status.Check { panic("boom") }

  func TestCollectKeepsTheOthersWhenOneHangsOrPanics(t *testing.T) {
  	ok := fixed{"node", []status.Check{{Section: status.SectionNode, State: status.OK, Line: "a"}}}
  	got := status.Collect(t.Context(), status.Scope{}, []status.Source{ok, blocking{}, panicking{}}, 20*time.Millisecond)
  	if len(got) != 3 {
  		t.Fatalf("got %d lines, want 3: %+v", len(got), got)
  	}
  	if got[0].Line != "a" {
  		t.Errorf("first line %q, want a", got[0].Line)
  	}
  	for _, c := range got[1:] {
  		if c.State != status.Warn || c.Line != texts.StatusNoAnswer || c.Section != status.SectionProblems {
  			t.Errorf("silent source gave %+v", c)
  		}
  	}
  	if got[1].Vars["component"] != "slow" || got[2].Vars["component"] != "broken" {
  		t.Errorf("components %q, %q", got[1].Vars["component"], got[2].Vars["component"])
  	}
  }

  func TestOrderIsBySectionAndStable(t *testing.T) {
  	in := []status.Check{
  		{Section: status.SectionProblems, Line: "p1"}, {Section: status.SectionNode, Line: "n1"},
  		{Section: status.SectionModels, Line: "m1"}, {Section: status.SectionNode, Line: "n2"},
  		{Section: status.SectionDelivery, Line: "d1"},
  	}
  	var got []texts.Key
  	for _, c := range status.Order(in) {
  		got = append(got, c.Line)
  	}
  	want := []texts.Key{"n1", "n2", "m1", "d1", "p1"}
  	if !slices.Equal(got, want) {
  		t.Errorf("order %v, want %v", got, want)
  	}
  }

  func TestVerdictCountsWarnAndFailNotOff(t *testing.T) {
  	for _, tc := range []struct {
  		states []status.State
  		want   status.State
  		n      int
  	}{
  		{[]status.State{status.OK, status.Off}, status.OK, 0},
  		{[]status.State{status.OK, status.Warn, status.Off, status.Warn}, status.Warn, 2},
  		{[]status.State{status.Warn, status.Fail}, status.Fail, 2},
  	} {
  		var cs []status.Check
  		for _, s := range tc.states {
  			cs = append(cs, status.Check{State: s})
  		}
  		if got, n := status.Verdict(cs); got != tc.want || n != tc.n {
  			t.Errorf("%v: got %s %d, want %s %d", tc.states, got, n, tc.want, tc.n)
  		}
  	}
  }
  ```

  `format_test.go`, table tests: `Bytes(0)="0 B"`, `Bytes(512)="512 B"`, `Bytes(45<<20)="45 MB"`,
  `Bytes(1288490188)="1.2 GB"`; `Duration(40*time.Second)="40 s"`, `Duration(12*time.Minute)="12 min"`,
  `Duration(3*time.Hour+5*time.Minute)="3 h 5 min"`, `Duration(76*time.Hour)="3 d 4 h"`; `Clock` of a time
  today in `Europe/Kyiv` is `"12:40"`, of yesterday `"2026-11-07 12:40"`.

- [x] **Step 2: Run** `go test ./status/`. Expected: FAIL (package does not exist).

- [x] **Step 3: Implement.**

  `status/status.go`:

  ```go
  // Package status is what the node says of itself for /status (R-OBS-11): each component reports its
  // own lines through Source, and Collect asks them all at once. A line's text and reason are fixed
  // texts with values, never an error's text, which can hold outside content.
  package status

  // Section is where a line goes; the constants are in the order the sections are shown.
  type Section string

  const (
  	SectionNode     Section = "node"
  	SectionModels   Section = "models"
  	SectionDelivery Section = "delivery"
  	SectionProblems Section = "problems"
  )

  var sections = []Section{SectionNode, SectionModels, SectionDelivery, SectionProblems}

  // State is how a line stands. Off is switched off by configuration and raises nothing.
  type State string

  const (
  	OK   State = "ok"
  	Warn State = "warn"
  	Off  State = "off"
  	Fail State = "fail"
  )

  // (Check, Scope and Source as in Interfaces above, with doc comments.)

  // Collect asks every source at once, each under its own timeout. A source that does not answer in
  // time or panics becomes one Warn line naming it, and the others are kept, in the sources' order.
  // A source must honour ctx; one that does not keeps its goroutine until it returns, and its answer is
  // dropped.
  func Collect(ctx context.Context, s Scope, sources []Source, timeout time.Duration) []Check {
  	results := make([]chan []Check, len(sources))
  	for i, src := range sources {
  		results[i] = make(chan []Check, 1)
  		go ask(ctx, s, src, timeout, results[i])
  	}
  	// One deadline for the wait, a little past the sources' own, so a source that ignores its
  	// context costs its line and no more.
  	wait, cancel := context.WithTimeout(ctx, timeout+timeout/2)
  	defer cancel()
  	var out []Check
  	for i, src := range sources {
  		select {
  		case cs, ok := <-results[i]:
  			if ok {
  				out = append(out, cs...)
  				continue
  			}
  		case <-wait.Done():
  		}
  		out = append(out, silent(src.Name()))
  	}
  	return out
  }

  // ask runs one source; a panic closes ch with nothing sent, which Collect reads as silence.
  func ask(ctx context.Context, s Scope, src Source, timeout time.Duration, ch chan<- []Check) {
  	defer func() {
  		if recover() != nil {
  			close(ch)
  		}
  	}()
  	cctx, cancel := context.WithTimeout(ctx, timeout)
  	defer cancel()
  	cs := src.Status(cctx, s)
  	if cctx.Err() != nil {
  		close(ch)
  		return
  	}
  	ch <- cs
  }

  func silent(name string) Check {
  	return Check{Section: SectionProblems, State: Warn, Line: texts.StatusNoAnswer, Vars: texts.Vars{"component": name}}
  }
  ```

  The deadline in `Collect` is a bound on waiting, not logic, so it is a context and not the injected
  clock; the test passes a short timeout. `Order` is `slices.SortStableFunc`
  by the index of the section in `sections`. `Verdict` returns `Fail` if any line is `Fail`, else `Warn`
  if any is `Warn`, else `OK`, with the count of `Warn` and `Fail` lines.

  `status/format.go`: `Bytes` in powers of 1024 with one decimal above 1 GB and none below
  (`"45 MB"`); `Duration` truncated to its two largest units as in the tests; `Clock` formats `15:04`
  when `t` is on `s.Now`'s day in `s.Location` (`clock.DayStart` of `internal/clock`), else
  `2006-01-02 15:04`.

  Add to `texts/texts.go` the key `StatusNoAnswer Key = "status.no_answer"` and to `allKeys`; to
  `en.yaml` `status.no_answer: "{component}: no answer"`, `ru.yaml` `"{component}: не ответил"`,
  `uk.yaml` `"{component}: не відповів"`.

- [x] **Step 4: Run** `go test -race ./status/ ./texts/`. Expected: PASS.

- [x] **Step 5: Commit** `status: the entity, the source interface and Collect (R-OBS-11)`.

### Task 2: `/status` in the agent

**Files:** Create `agent/status.go`, `agent/status_test.go`, `config/status.go`. Modify
`agent/agent.go` (`Deps.StatusSources`), `agent/commands.go` (the row), `config/agent.go`,
`config/config.go`, `texts/texts.go` (`Escape` and the keys below), `texts/lang/*.yaml`,
`docs/architecture/05-code-map.md` (the agent row: `/status`; `agent --> status`).

**Interfaces:**
- Consumes: `status.Collect`, `status.Order`, `status.Verdict`, `status.Scope`, `status.Check`.
- Produces: `agent.Deps.StatusSources []status.Source`; in `config`,
  `StatusSources() do.Provider[[]status.Source]`, whose list later tasks add one line each to;
  `texts.Escape(s string) string`.

Keys added here (English; Russian and Ukrainian in the same commit):

| Key | English |
|---|---|
| `status.verdict_ok` | `All well` |
| `status.verdict_warn` | `Needs a look: {n}` |
| `status.verdict_fail` | `Not working: {n}` |
| `status.section_node` | `Node` |
| `status.section_models` | `Models` |
| `status.section_delivery` | `Delivery` |
| `status.section_problems` | `Off and errors` |
| `status.since` | `since {time}` |
| `status.session` | `This session: {models}` |
| `status.session_fallback` | `This session: {models}; the last turn was answered by {model}` |
| `status.session_none` | `This session: no model fits` |
| `status.day` | `Today for you: turns {turns}, tokens {tokens}; the node spent {cost}` |
| `status.reason_limit_near` | `past 80% of a daily limit` |
| `status.reason_limit_reached` | `a daily limit is reached` |

- [x] **Step 1: Write the failing tests** in `agent/status_test.go`, on the agent's test env
  (`newEnv`, `e.turn`, `message(owner, …)` of `agent_test.go`), as `TestLastCommandShowsTheTurnBefore`
  does:

  ```go
  // fakeSource answers /status with fixed lines.
  type fakeSource struct {
  	name   string
  	checks []status.Check
  }

  func (f fakeSource) Name() string                                        { return f.name }
  func (f fakeSource) Status(context.Context, status.Scope) []status.Check { return f.checks }

  // statusEnv is the test env with the given sources.
  func statusEnv(t *testing.T, cfg Config, srcs ...status.Source) *env {
  	e := newEnv(t, cfg, nil)
  	e.deps.StatusSources = srcs
  	e.a = New(overDefaults(cfg), e.deps)
  	return e
  }

  func TestStatusShowsVerdictSectionsAndReasons(t *testing.T) {
  	since := time.Date(2026, 11, 8, 9, 15, 0, 0, time.UTC)
  	e := statusEnv(t, Config{}, fakeSource{name: "skills", checks: []status.Check{
  		{Section: status.SectionNode, State: status.OK, Line: texts.StatusNoAnswer, Vars: texts.Vars{"component": "x"}},
  		{Section: status.SectionProblems, State: status.Warn, Line: texts.StatusNoAnswer,
  			Vars: texts.Vars{"component": "my_skill"}, Since: since},
  	}})
  	e.now = since.Add(3 * time.Hour)
  	out := e.turn(t, message(owner, "Igor", "/status"))
  	if len(out) != 1 {
  		t.Fatalf("out %+v", out)
  	}
  	for _, want := range []string{"⚠️ Needs a look: 1", "## Node", "## Models", "## Off and errors",
  		`my\_skill: no answer`, "since 09:15", "This session: "} {
  		if !strings.Contains(out[0].Text, want) {
  			t.Errorf("reply lacks %q:\n%s", want, out[0].Text)
  		}
  	}
  	if strings.Contains(out[0].Text, "## Delivery") {
  		t.Error("a section with no line is shown")
  	}
  }

  // /status spends no model and joins no history; a family member gets the refusal.
  func TestStatusIsForAdminsAndLeavesNoNote(t *testing.T) {
  	e := statusEnv(t, Config{})
  	e.smart.push(say("Hello."))
  	e.turn(t, message(owner, "Igor", "hi"))
  	calls, history := len(e.smart.requests()), len(e.state(t).Messages)
  	e.turn(t, message(owner, "Igor", "/status"))
  	if len(e.smart.requests()) != calls || len(e.state(t).Messages) != history {
  		t.Fatal("/status reached the model or the history")
  	}
  	e.admin = false
  	out := e.turn(t, message("telegram:user:7", "Olena", "/status"))
  	if len(out) != 1 || out[0].Text != replyTasksNotAvailable {
  		t.Fatalf("a family member got %+v", out)
  	}
  }
  ```

  Three more, in the same form:
  - `TestStatusSessionLineNamesTheFallback`: record a turn of the conversation with
    `Route: []turns.Attempt{{Provider: "p", Model: "a", Error: "timeout"}, {Provider: "p", Model: "b"}}`
    through `e.deps.Turns.Record`; the reply holds `the last turn was answered by b`.
  - `TestStatusDayAgainstTheLimits`: `Config{DailyTurns: 10}` and 9 recorded `ok` turns today: the line
    holds `turns 9/10` and `past 80% of a daily limit`, verdict `⚠️`; with 10: `a daily limit is reached`,
    verdict `❌`; with both limits 0: `turns 9`, `✅`.
  - `TestStatusInTheSendersLanguage`: a sender whose message carries `Lang: "ru"` gets `## Нода`.

  And in `texts/texts_test.go`: `TestEscape` (`Escape("my_skill [x]") == `my\_skill \[x\]``).

- [x] **Step 2: Run** `go test ./agent/ ./texts/`. Expected: FAIL.

- [x] **Step 3: Implement.**

  `texts.Escape` exports `escapeMarkdown` with a doc comment: for a value put into a message's text.

  `agent/agent.go`, in `Deps`:

  ```go
  // StatusSources are the components /status asks (R-OBS-11); none when unset.
  StatusSources []status.Source
  ```

  `agent/commands.go`, after `/last`:

  ```go
  {"/status", a.showStatus, map[transport.Lang]string{
  	transport.LangDefault: "How the node is doing", transport.LangRussian: "Как дела у ноды",
  	transport.LangUkrainian: "Як справи у вузла"}},
  ```

  `agent/status.go`:

  ```go
  // statusTimeout bounds each source's answer, so /status stays quick when one hangs.
  const statusTimeout = 2 * time.Second

  // limitNear is the share of a daily limit past which the day's line warns.
  const limitNear = 0.8

  // statusMarks are the state marks in front of a line.
  var statusMarks = map[status.State]string{status.OK: "✅", status.Warn: "⚠️", status.Off: "⏸", status.Fail: "❌"}

  // showStatus shows an admin how the node is doing, with no model and no note in the history (R-OBS-11).
  func (a *Agent) showStatus(ctx context.Context, t *turn, _ string) error {
  	if err := a.setAdmin(ctx, t); err != nil {
  		return err
  	}
  	if !t.admin {
  		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
  		return nil
  	}
  	s := status.Scope{Conv: t.event.Conv, Principal: t.msg.From, Now: t.now, Location: a.cfg.Location}
  	checks := append(a.ownStatus(ctx, t, s), status.Collect(ctx, s, a.d.StatusSources, statusTimeout)...)
  	t.reply(renderStatus(status.Order(checks), a.speaker(ctx, t), s))
  	return nil
  }

  // ownStatus is what only the agent knows: the session's models and the day against the limits.
  func (a *Agent) ownStatus(ctx context.Context, t *turn, s status.Scope) []status.Check {
  	return []status.Check{a.sessionLine(ctx, t), a.dayLine(ctx, t, s)}
  }
  ```

  - `sessionLine`: `a.d.Router.Route(ctx, router.TaskReq{Kind: router.TaskMessage, Tools:
    len(a.toolSpecs(t)) > 0, Privacy: t.state.privacy()})`, the same request `steps` makes. An error is
    `Fail` with `status.session_none`. The models are the candidates' `Model`s joined by ", ". When
    `a.d.Turns.Last(ctx, t.event.Conv)` has a route whose answering attempt (the last without `Error`)
    is not the first candidate, the line is `status.session_fallback` with `{model}` that attempt's model
    and state `Warn`. `turns.ErrNoTurn` is no fallback.
  - `dayLine`: `a.d.Turns.Totals(ctx, clock.DayStart(s.Now, a.cfg.Location), t.msg.From, "")`, and the
    node's spend `a.d.Turns.SpentSince` from the same start. `{turns}` is `"9/10"`, or `"9"` when the
    limit is 0; `{tokens}` likewise; `{cost}` is `fmt.Sprintf("$%.2f", spent)`. The state is `Fail` when
    a limit is reached (`!under`), `Warn` past `limitNear` of one, else `OK`. A totals error: `Warn` with
    `status.no_answer` and `{component}` `"turns"`.
  - `renderStatus` is pure: the verdict line (`statusMarks[v] + " " + verdict text`), then per section
    that has lines a blank line, `"## " + title` and one `"- " + mark + " " + line` per check. A line's
    text is `sp.Text(c.Line, escaped(c.Vars))`; a reason adds `" — " + sp.Text(c.Reason, escaped(c.Vars))`;
    a non-zero `Since` on a line that is not `OK` adds `" (" + sp.Text(texts.StatusSince,
    {"time": status.Clock(c.Since, s)}) + ")"`. `escaped` returns a copy with every value through
    `texts.Escape`. Keep each function under the `funlen` and `gocognit` limits: one for the verdict, one
    for a section, one for a line.

  `config/status.go`:

  ```go
  // StatusSources provides the components /status asks (R-OBS-11): each part that is on adds its own.
  func StatusSources() do.Provider[[]status.Source] {
  	return func(i do.Injector) ([]status.Source, error) {
  		return []status.Source{}, nil
  	}
  }
  ```

  Register `do.Lazy(StatusSources())` in `Core` (`config/config.go`) and set
  `StatusSources: do.MustInvoke[[]status.Source](i)` in `agentParts`.

- [x] **Step 4: Run** `go test -race ./agent/ ./texts/ ./status/ ./config/` and `make lint`. Expected: PASS.

- [x] **Step 5: Commit** `agent: /status shows an admin how the node is doing (R-OBS-11)`.

### Task 3: The node's own lines

**Files:** Create `status/proc/proc.go`, `status/proc/proc_test.go`. Modify `config/status.go`,
`texts/texts.go`, `texts/lang/*.yaml`, `docs/architecture/05-code-map.md` (row `status/proc`).

**Interfaces:**
- Consumes: `status.Check`, `status.Bytes`, `status.Duration`, `internal/buildinfo.Read`.
- Produces: `proc.New(start time.Time, files proc.Files, opts ...proc.Option) *proc.Source` with
  `type Files struct{ DB, Logs string }` (`Logs` "" when logs are off) and options
  `WithMemory(func() (rss, peak int64, ok bool))`, `WithLimit(func() int64)`, `WithBuild(buildinfo.Info)`
  for tests. `Name()` is `"node"`.

Keys: `status.version` `ganglion {version}, commit {commit} of {date}, {go} {platform}`;
`status.version_modified` `ganglion {version}, commit {commit} of {date}, modified, {go} {platform}`;
`status.version_unknown` `ganglion {version}, commit unknown, {go} {platform}`; `status.uptime` `Running for {uptime}`; `status.memory` `Memory {rss}, peak {peak};
GOMEMLIMIT {limit}`; `status.memory_no_limit` `Memory {rss}, peak {peak}; no GOMEMLIMIT`;
`status.reason_memory_high` `above 90% of the soft limit`; `status.files` `Database {db}, logs {logs}`;
`status.files_no_logs` `Database {db}, logs off`.

- [x] **Step 1: Write the failing tests:** the version line with a commit (`status.version`, `{commit}`
  the first 7 characters, `{date}` `2006-01-02`), with `Modified` (`status.version_modified`), and without
  VCS (`status.version_unknown`); `{platform}` is `OS/Arch`; uptime from a fixed start and `Scope.Now`; memory `OK` with
  no limit (`memory_no_limit`), `Warn` with `reason_memory_high` when RSS is 95% of the limit; no memory
  line when the reader says `ok == false`; the file sizes of a temp database with a `-wal` file beside it
  (both counted) and a temp log directory of two files.

- [x] **Step 2: Run** `go test ./status/proc/`. Expected: FAIL.

- [x] **Step 3: Implement.** The default memory reader parses `VmRSS` and `VmHWM` (kB) from
  `/proc/self/status`; it returns `ok == false` where the file is missing. The default limit is
  `debug.SetMemoryLimit(-1)`; `math.MaxInt64` means none. The log size walks `files.Logs` one level
  (rotated files are flat, ADR-0043). All lines are `SectionNode`. Register in `config/status.go`:

  ```go
  b := do.MustInvoke[Bootstrap](i)
  sources := []status.Source{
  	proc.New(time.Time(do.MustInvoke[ProcessStart](i)), proc.Files{DB: b.DB, Logs: b.LogDir}),
  }
  ```

- [x] **Step 4: Run** `go test -race ./status/... ./config/`. Expected: PASS.

- [x] **Step 5: Commit** `status/proc: the node's version, uptime, memory and files`.

### Task 4: Models and failed turns from the turns log

**Files:** Create `turns/status.go`, `turns/status_test.go`. Modify `config/status.go`, `texts`,
`05-code-map.md` (`turns --> status`).

**Interfaces:**
- Produces: `func (l *Log) Status(ctx context.Context, s status.Scope) []status.Check`, `Name()` `"turns"`;
  `func (l *Log) ModelsSeen(ctx context.Context, since time.Time) ([]ModelSeen, error)` with
  `type ModelSeen struct{ Model string; Answered, Failed time.Time; Kind string }`.

Keys: `status.model_answered` `{model}: answered {time}`; `status.model_silent` `{model}: no answer in 7
days`; `status.reason_model_failed` `failed {time} ({kind})`; `status.turns_failed` `Failed turns in the
last day: {n}`.

- [x] **Step 1: Write the failing tests** on a temp database with recorded turns:
  - a model that answered at 12:40 and failed at 12:00: `OK`, "answered 12:40";
  - one that failed at 12:50 after answering at 12:40: `Warn`, reason with `timeout`;
  - every model failing last: each `Fail`;
  - turns older than 7 days are not read;
  - two `error` outcomes and one `no_model` in the last day, one older: `Warn`, `{n}` 3.

- [x] **Step 2: Run** `go test ./turns/`. Expected: FAIL.

- [x] **Step 3: Implement.** `ModelsSeen` in one query over `json_each(route)`:

  ```sql
  SELECT json_extract(a.value, '$.model') AS model,
         MAX(CASE WHEN COALESCE(json_extract(a.value, '$.error'), '') = '' THEN t.started_ms END),
         MAX(CASE WHEN COALESCE(json_extract(a.value, '$.error'), '') <> '' THEN t.started_ms END)
  FROM turns t, json_each(t.route) a
  WHERE t.started_ms >= ?
  GROUP BY model ORDER BY model
  ```

  and a second query for the kind of each model's newest failure. A model is `Warn` when `Failed` is
  after `Answered`, `Fail` when that holds for every model; the line is `SectionModels`. Failed turns
  are `outcome IN ('error', 'no_model')` since `s.Now - 24h`, `SectionProblems`, `Warn` when above 0.
  Append `do.MustInvoke[*turns.Log](i)` to the sources in `config/status.go`.

- [x] **Step 4: Run** `go test -race ./turns/ ./config/`. Expected: PASS.

- [x] **Step 5: Commit** `turns: /status reads each model's last answer and the failed turns`.

### Task 5: Telegram's polling

**Files:** Create `transport/telegram/health.go`, `transport/telegram/health_test.go`. Modify
`transport/telegram/transport.go` (record a poll and a stop), `config/status.go`, `config/config.go`,
`texts`, `05-code-map.md` (`transport/telegram --> status`).

**Interfaces:**
- Produces (as built): `telegram.Health` with `NewHealth(now)`, `Status` and `Name()` `"telegram"`, given to the
  transport with `WithHealth`, because the transport waits for the agent's command hints and the agent for its
  sources; in `config`, `type Watched []status.Source`, `TransportWatch() do.Provider[Watched]`
  choosing `watchTelegram` or `watchNone`.

Keys: `status.telegram` `Telegram: polled {time}`; `status.telegram_waiting` `Telegram: not polled yet`;
`status.reason_poll_stale` `no successful poll for {for}`; `status.reason_conflict` `polling stopped:
another poller or a webhook holds the bot`; `status.reason_unauthorized` `polling stopped: the token was
refused`.

- [x] **Step 1: Write the failing tests** with `telegramtest`: after one poll the line is `OK` with its
  time; with the clock moved 3 minutes and no poll since, `Warn` `reason_poll_stale` "3 min"; after the
  fake answers `409`, `Fail` `reason_conflict` with `Since` the stop; after `401`, `Fail`
  `reason_unauthorized`. A quiet poll (no updates) counts as a poll.

- [x] **Step 2: Run** `go test ./transport/telegram/`. Expected: FAIL.

- [x] **Step 3: Implement.** `health.go` holds `type health struct{ mu sync.Mutex; polled, stopped
  time.Time; stop texts.Key }`, a field of `Transport`. `Run` calls `t.health.polled = t.now()` after
  every `getUpdates` that returned no error, and `onError` records the stop for `409` and `401` before it
  returns. `Status` reads under the mutex; the stale bound is a constant `pollStale = 2 * time.Minute`.
  In `config`: `TransportWatch()` with `choose[Watched](telegramOn, watchTelegram, watchNone)`,
  `TelegramWatch()` returning `Watched{do.MustInvoke[*telegram.Transport](i)}`, the names in the
  constants block, registration in `Core`, and `sources = append(sources, do.MustInvoke[Watched](i)...)`.

- [x] **Step 4: Run** `go test -race ./transport/telegram/ ./config/`. Expected: PASS.

- [x] **Step 5: Commit** `telegram: /status shows the last poll and a stopped poller`.

### Task 6: The queues

**Files:** Create `queue/sqlqueue/status.go`, `queue/sqlqueue/status_test.go`. Modify `config/status.go`,
`texts`, `05-code-map.md` (`queue/sqlqueue --> status`).

**Interfaces:**
- Produces: `func (q *Queue) Status(ctx context.Context, s status.Scope) []status.Check`, `Name()`
  `"queues"`.

Keys: `status.queues` `Inbox: {inbox} waiting; outbox: {outbox} waiting`; `status.reason_queue_old` `the
oldest has waited {for}`; `status.reason_undelivered` `{n} undelivered in the last day`.

- [x] **Step 1: Write the failing tests:** an empty queue is `OK` "0 waiting"; an inbox row pending
  since 2 minutes is `Warn` `reason_queue_old` "2 min"; an inbox row whose `at_ms` is in the future is
  not waiting; an outbox row `failed` with `done_ms` within a day is `Warn` `reason_undelivered` "1";
  one failed two days ago is not counted.

- [x] **Step 2: Run** `go test ./queue/sqlqueue/`. Expected: FAIL.

- [x] **Step 3: Implement** one line in `SectionDelivery`: counts of `inbox` with `status = 'pending' AND
  at_ms <= now` and of `outbox` with `status = 'pending'`; the oldest of each (`at_ms`, `created_ms`); the
  failed count with `done_ms >= now - 24h`. `Warn` past `queueOld = time.Minute` or with any failed.
  Append the queue to the sources in `config/status.go`.

- [x] **Step 4: Run** `go test -race ./queue/sqlqueue/ ./config/`. Expected: PASS.

- [x] **Step 5: Commit** `sqlqueue: /status shows what waits and what was not delivered`.

### Task 7: Skills not loaded, modules fallen back

**Files:** Create `skills/status.go`, `skills/status_test.go`, `modstore/status.go`,
`modstore/status_test.go`. Modify `skills/skills.go` (`Problem.Kind`), `modstore/store.go` (remember a
fallback), `config/agent.go` (`Skilled.Problems`), `config/status.go`, `texts`, `05-code-map.md`.

**Interfaces:**
- Produces:
  ```go
  // skills
  type ProblemKind string // ProblemShape, ProblemRead, ProblemFrontmatter, ProblemScript, ProblemBuiltinName
  type Problem struct { Skill, Reason string; Kind ProblemKind } // Reason stays for the log
  type Refused struct { Problems []Problem; Loaded int; Since time.Time } // implements status.Source, Name "skills"
  // modstore
  func (s *Store) Status(ctx context.Context, sc status.Scope) []status.Check // Name "modules"
  ```

Keys: `status.skills` `Skills: {n} loaded`; `status.skill_refused` `Skill {skill} is not loaded`;
`status.reason_skill_shape` `not a directory`; `status.reason_skill_read` `its SKILL.md cannot be read`;
`status.reason_skill_frontmatter` `its frontmatter is not valid`; `status.reason_skill_script` `its
script breaks a rule`; `status.reason_skill_builtin` `a built-in skill has its name`;
`status.module_fallback` `Module {module}: the built-in build runs`; `status.reason_module_failed` `the
stored version {version} failed its check at start`.

- [x] **Step 1: Write the failing tests:**
  - `skills.Load` gives each problem its kind: a file in place of a directory (`shape`), a directory with
    no `SKILL.md` (`read`), a name that is not the directory's (`frontmatter`), a script that is not
    executable (`script`), a workspace skill named like a built-in (`builtin`);
  - `Refused.Status`: one `OK` line "Skills: 3 loaded", then one `Warn` line per problem with its
    reason key and `Since`; no line ever carries `Problem.Reason`;
  - `modstore`: a stored version whose file is gone at `Open` makes `Status` give `Warn`
    `module_fallback` with `{version}`; a clean start gives no line.

- [x] **Step 2: Run** `go test ./skills/ ./modstore/`. Expected: FAIL.

- [x] **Step 3: Implement.** Set `Kind` where `Load` and `loadDir` append a problem: `loadDir` returns a
  typed `*loadError{kind ProblemKind; err error}`, unwrapped in `Load`. `modstore.load` records
  `s.fellBack[b.Name] = fallback{version: v.Version, at: s.now()}` (a map under `activeMu`) where it logs
  the fallback. `config.Skills` keeps the problems and the load time in `Skilled`, and
  `config/status.go` appends `skills.Refused{Problems: sk.Problems, Loaded: len(sk.Set.Names()), Since:
  time.Time(do.MustInvoke[ProcessStart](i))}` and `do.MustInvoke[*modstore.Store](i)`. As built: no `agentOn` branch, since only the agent
  asks for the list.

- [x] **Step 4: Run** `go test -race ./skills/ ./modstore/ ./config/`. Expected: PASS.

- [x] **Step 5: Commit** `skills, modstore: /status names what is not loaded and why`.

### Task 8: The scheduler

**Files:** Create `schedule/status.go`, `schedule/status_test.go`. Modify `schedule/schedule.go`
(remember the last tick, its failure and the last lag), `config/status.go`, `texts`, `05-code-map.md`.

**Interfaces:**
- Produces: `func (s *Scheduler) Status(ctx context.Context, sc status.Scope) []status.Check`, `Name()`
  `"scheduler"`.

Keys: `status.jobs` `Jobs: {n}, next {time}`; `status.jobs_none` `Jobs: none`;
`status.reason_sched_lag` `the last run was {lag} late`; `status.reason_sched_failing` `its ticks fail`;
`status.reason_sched_stalled` `no tick for {for}`.

- [ ] **Step 1: Write the failing tests** with the injected `now` and `after`:
  - two jobs, an hourly task and a reminder at 18:00, at 17:10: `OK` "Jobs: 2, next 18:00";
  - no jobs: `OK` `jobs_none`;
  - a run put 90 s after its slot: `Warn` `reason_sched_lag` "1 min";
  - a tick that failed last: `Warn` `reason_sched_failing` with `Since` that tick;
  - no tick for 3 minutes: `Warn` `reason_sched_stalled`.

- [ ] **Step 2: Run** `go test ./schedule/`. Expected: FAIL.

- [ ] **Step 3: Implement.** `Scheduler` gains `health struct{ mu sync.Mutex; ticked, failed time.Time;
  lag time.Duration }`, set in `Run` after each `Tick` and in `put` with the lag. The next run is the
  smallest of each active job's next slot after `now` (a one-shot's `At` when it has not run; a cron's
  `spec.Next(max(last, now), loc)`), through the same `due` rules. Bounds: `lagWarn = time.Minute`,
  `tickStale = 2 * time.Minute`. Append the scheduler to the sources in `config/status.go` inside the
  agent's branch.

- [ ] **Step 4: Run** `go test -race ./schedule/ ./config/`. Expected: PASS.

- [ ] **Step 5: Commit** `schedule: /status shows the jobs, the next run and a late or failing tick`.

### Task 9: Chatlab's check and its seeded fault

**Files:** Create `tools/chatlab/scenarios/mech-status.yaml`,
`tools/chatlab/scenarios/skills/bad-name/SKILL.md`, `tools/chatlab/faults/status-verdict.patch`. Modify
`tools/chatlab/faults/faults.txt`.

- [ ] **Step 1: Write the scenario.** `bad-name/SKILL.md` has `name: other-name`, so it is refused as
  `frontmatter`.

  ```yaml
  # yaml-language-server: $schema=../../../schemas/chatlab-scenario.schema.json
  name: mech-status
  why: >-
    R-OBS-11: /status answers with no model through the real core and the Bot API, with the four
    sections; a skill that is not loaded makes the verdict a warning and is named with its reason.
  covers:
    - agent/status.go
    - status/**
    - skills/status.go
  model: scripted
  skills: [skills/bad-name]
  script: []
  steps:
    - say: "/status"
    - facts:
        reply: {contains: "Needs a look: 1"}
        history: {not_contains: "/status"}
    - facts:
        reply: {contains: "bad-name"}
    - facts:
        reply: {contains: "frontmatter"}
    - facts:
        reply: {contains: "Delivery"}
  ```

- [ ] **Step 2: Run** `make lab LAB=mech-status`. Expected: PASS, 3 of 3.

- [ ] **Step 3: Seed the fault.** `status-verdict.patch` makes `status.Verdict` return `OK, 0` always;
  add `status-verdict mech-status` to `faults.txt`. Run `make lab-faults`. Expected: `FAULT
  status-verdict: red, as it must be`, and every other fault still red.

- [ ] **Step 4: Commit** `chatlab: /status answers with its sections, and a broken verdict turns it red`.

### Task 10: Closing

**Files:** `docs/README.md`, `docs/plans/p1-all-in-one.md` (3.38 done), this plan,
`docs/architecture/05-code-map.md`.

- [ ] **Step 1: Measure** with `make memory`; record the binary and RSS beside reply context's 46.2 MB
  and 45.5 MB (ADR-0056: compared, not held to a line).
- [ ] **Step 2: Run** `make test`, `make lint`, `make lab` for what changed. Ask the owner before any full run.
- [ ] **Step 3: Request a whole-branch review** with superpowers:requesting-code-review; fix or record
  each finding.
- [ ] **Step 4: The owner's look on the test bot:** `/status` on a phone, in Russian; the session's
  models; a skill broken on purpose shows; the verdict reads at a glance.
- [ ] **Step 5: Update "Where we are"** in `docs/README.md` (3.38 closed, the deferred minors) and the
  code map's statuses.
- [ ] **Step 6: Commit** `docs: /status done`, and ask the owner about the merge.
