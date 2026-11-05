# P1 · Live turns — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.
>
> **Execution (owner, 2026-11-05):** hybrid, in a new session. Tasks are implemented
> inline; a fresh reviewer, given the task's diff and its section of this plan, checks
> tasks 4 and 5 (the turn loop and the security rules), and one more reviews the whole
> branch at the end.

**Goal:** a message written while the agent works joins the running turn at the next
boundary; a reaction shows work that takes long and that an addition was taken; work with
several steps shows a plan edited in place.

**Architecture:** `agent.steps` becomes the one place that takes additions, on both routes:
the API route between calls, the CLI route by ending the run at a tool boundary and starting
a new one with the whole history. Reactions and the plan's mid-turn messages stay out of the
turn's transaction: they are progress, not the reply.

**Tech stack:** Go 1.27, SQLite through `sqlitedb`, the Telegram Bot API's
`setMessageReaction` and `expandable_blockquote`. No new dependency.

**Spec:** [p1-live-turns-design.md](p1-live-turns-design.md), approved by the owner on
2026-11-05. Read it with this plan.

## Global Constraints

- All documentation, comments and doc comments are in English; [conventions](../conventions.md) apply.
- Spec first, then code. A decision that changes goes to the spec before the code changes.
- Architecture docs change in the same commit as the code they describe
  (`docs/architecture/05-code-map.md`, the matching level page).
- A metric and a log line come in the same commit as what can fail, wait, retry or cost
  (R-OBS-8): labels are low-cardinality, never a conversation, a principal or message text.
- ADRs stay Proposed until the owner accepts them.
- No new Go dependency. No `time.Sleep` in tests: inject clocks.
- A full chatlab run is never automatic, including after the prompt changes in task 9: use
  `make lab` for what changed and ask the owner before `make lab-all` (about 900,000 tokens).
- Commit only with the owner's go-ahead for this plan's execution; never on `master`.

## Review Focus

Failure modes the design implies and no task would test by itself. Each has its test in the
named task.

1. **A command right behind an addition.** "Also check logs", then `/new`: the addition joins,
   `/new` waits and then archives the session with the finished answer in it (task 4).
2. **An addition with only a file, or a forward with a URL.** Described, never dropped (R-DLV-10);
   a forward's URL does not become known, as at a turn's start (task 4).
3. **An addition after a call was parked.** The press later runs exactly that call once; the
   addition grants nothing and does not cancel it (task 4).
4. **Telegram refuses a reaction.** The message was deleted, or a `429`: the answer still goes,
   reactions pause and resume, nothing retries in a loop (task 7).
5. **An earlier answer longer than one Telegram message.** Every part collapses, none loses text,
   and a quote inside it does not fail the send with a nested entity (task 3).

---

### Task 0: Measure the cost, before anything is built

Owner's bar (design 3.3): ending the CLI run stays if a turn with an addition costs at most twice
what the two separate turns cost together. **If it fails, stop and bring the numbers to the owner
before task 1.**

**Files:**
- Create: `docs/research/2026-11-05-cost-of-an-addition.md`
- Scratch only, not committed: a shell script in the session's scratchpad

- [x] **Step 1: Run the three cases on the CLI route, 3 repeats each, with Sonnet**

Use the node's real system prompt size and flags (`-p --model sonnet --output-format json
--no-session-persistence --tools "" --setting-sources ""`), with a history of about 2,000 tokens.
Runs are left to finish, so usage is reported.

| Case | Runs |
|---|---|
| A. Two separate turns | answer Q1; answer Q2 with Q1 and its answer in the history |
| B. An addition | run with Q1 that reads one fake tool result in its history; a new run with the same history plus Q2 |
| C. Stdin | `--input-format stream-json --output-format stream-json --verbose`, an MCP-free prompt; write Q2 to stdin while Q1 is running; record when it is picked up |

Record `input_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`, `output_tokens`,
`total_cost_usd` and wall time.

- [x] **Step 2: Write the note and state the verdict**

Follow the shape of `docs/research/2026-11-04-language-of-replies.md`. State B ÷ A against the bar,
whether the cache held across runs, and what C showed.

Expected: one line "ending the run: within the bar" or "over the bar: stop".

- [x] **Step 3: Commit**, message `docs: what an addition costs on the CLI route`.

---

### Task 1: Spec and ADRs

**Files:**
- Modify: `docs/spec.md` (R-AGT-1, R-DLV-4, a new group `R-LIVE-1..n` in 7.2, "Not now"), `docs/plans/p1-all-in-one.md`, `docs/plans/p1-m3d-host-commands-design.md`
- Create: `docs/adr/0050-additions-join-a-running-turn.md`, `docs/adr/0051-status-reactions-outside-the-outbox.md`, `docs/adr/0052-a-plan-is-delivered-during-its-turn.md`
- Modify: `docs/adr/README.md`

- [x] **Step 1: Update the spec**

R-AGT-1 stays one turn at a time and gains "a turn may take the messages behind it" (R-LIVE-1..). R-DLV-4
gains the exception for the earlier answer and the plan. "Not now" gains `/stop`, a reaction chosen by the
model, text a CLI run wrote before it was ended (`stream-json`), and reactions in groups (this last replaces
the private-chat half of the entry at spec line 1268).

- [x] **Step 2: Add the milestone to the phase plan**

Section `#### M3 · Live turns`, before M3d, tasks 3.28 additions, 3.29 reactions, 3.30 the plan's display.
M3d's 3.24 keeps only the press; note in the M3d design's 7.1 that the display moved here.

- [x] **Step 3: Write the three ADRs from `docs/adr/template.md`**, Status Proposed, each with at least two options and a Security section:

- 0050: end the run vs stdin (with task 0's numbers); the taking rule (design 3.4); `Taken` in `Complete`; a
  rerun's call numbers. Note there: `c.Key()` may name a different call after a rerun with additions, and it
  fails safe, since a key that returns is refused or deduplicated rather than run twice. The estimate of an
  ended run's tokens, and the text an ended run loses.
- 0051: outside the outbox vs through it; the threshold is a starting value.
- 0052: `Outbox.Enqueue` during a turn vs at `Complete`; the plan table.

- [x] **Step 4: Ask the owner to accept the ADRs.** Do not start task 2 on a rejected one.

- [x] **Step 5: Commit**, message `docs: live turns in the spec, the phase plan and three ADRs`.

---

### Task 2: The queue hands over what waits behind a turn

**Files:**
- Modify: `queue/queue.go`, `queue/sqlqueue/inbox.go`, `queue/sqlqueue/telemetry.go`; any `queue.Queue` fake in `core/loop_test.go`
- Create: `queue/sqlqueue/migrations/004_inbox_taken_by.sql` (`ALTER TABLE inbox ADD COLUMN taken_by TEXT`)
- Test: `queue/sqlqueue/inbox_test.go`

**Interfaces:**
- Produces:
  - `queue.Queue.Behind(ctx context.Context, e event.Event, limit int) ([]event.Event, error)`: pending events of `e.Conv` with a later seq than `e`, oldest first.
  - `queue.StateUpdate.Taken []string`: ids of events the turn took.

- [ ] **Step 1: Write the failing tests**

```go
func TestBehindListsOnlyLaterEventsOfTheConversation(t *testing.T)
// put a1, a2, a3 in conv a and b1 in conv b; lease a1
// Behind(a1, 10) == [a2, a3]; Behind(a1, 1) == [a2]; Behind(a3, 10) is empty

func TestCompleteMarksTakenEventsDoneInOneTransaction(t *testing.T)
// Complete(lease a1, out, StateUpdate{Conv: a, Taken: [a2]})
// a2 is done with taken_by = a1; the next Lease returns a3

func TestFailedCompleteLeavesTakenEventsPending(t *testing.T)
// complete with a lost lease: ErrLeaseLost; a2 stays pending, and leasing again returns a1's successor in order

func TestCompleteIgnoresTakenFromAnotherConversation(t *testing.T)
// Taken: [b1] leaves b1 pending
```

- [ ] **Step 2: Run:** `go test -race ./queue/...` Expected: FAIL, `Behind` undefined.

- [ ] **Step 3: Implement**

`Behind` finds `e`'s seq by id. In `Complete`, after `markDone`: `UPDATE inbox SET status = 'done', done_ms = ?, taken_by = ? WHERE id = ? AND conv = ? AND status = 'pending'` per taken id. A row that changed nothing is not an error.

- [ ] **Step 4: Run:** `go test -race ./queue/... ./core/...` Expected: PASS.

- [ ] **Step 5: Commit**, message `queue: a turn can take the events behind it`, with the code map.

---

### Task 3: An earlier answer goes out as a collapsed quote

**Files:**
- Modify: `event/event.go`, `transport/telegram/transport.go`, `transport/telegram/markdown.go`
- Test: `transport/telegram/transport_test.go`, `transport/telegram/markdown_test.go`

**Interfaces:**
- Produces: `event.Outgoing.Superseded bool` (`json:",omitempty"`): an answer overtaken by a later message; the transport shows it as it can.
- Produces: `collapsed(text string, ents []entity) []entity` in `markdown.go`.

- [ ] **Step 1: Write the failing tests**

```go
func TestCollapsedWrapsAllTextInOneExpandableQuote(t *testing.T)
// "short" -> one expandable_blockquote at 0, len(text), whatever the length

func TestCollapsedRemovesQuotesInside(t *testing.T)
// a source with a blockquote and bold: no blockquote or expandable_blockquote entity
// inside remains, bold stays, and the outer quote covers everything

func TestSupersededLongAnswerCollapsesEveryPart(t *testing.T) // Review Focus 5
// a 9,000-unit answer through the fake Bot API: each sent part has one expandable_blockquote
// over its whole text, and the parts join to the source
```

- [ ] **Step 2: Run:** `go test -race ./transport/telegram/` Expected: FAIL.

- [ ] **Step 3: Implement.** In `Send`, after `render`, apply `collapsed` when `msg.Superseded`. A superseded message has no reply parameters of its own beyond `ReplyTo`, no buttons. Check where a long text is split (`split.go`) and apply it per part.

- [ ] **Step 4: Run** the same command. Expected: PASS.

- [ ] **Step 5: Commit**, message `telegram: a superseded answer goes out as a collapsed quote`.

---

### Task 4: Additions in the turn loop (API route)

**Files:**
- Create: `agent/additions.go`, `agent/additions_test.go`
- Modify: `agent/agent.go` (Deps), `agent/turn.go`, `agent/stages.go`, `agent/tools.go`, `agent/limits.go`, `agent/telemetry.go`, `agent/prompt.go`, `config/agent.go`

**Interfaces:**
- Consumes: `queue.Queue.Behind`, `StateUpdate.Taken` (task 2), `Outgoing.Superseded` (task 3), the existing `Deps.Outbox`.
- Produces:
  - `agent.Inbox` interface `{ Behind(ctx, event.Event, int) ([]event.Event, error) }`, field `Deps.Inbox`, and `NoInbox` for parts of the agent with no queue.
  - On `turn`: `taken []string`, `takenUsers []provider.Message`, `quotes int`.
  - `type additionMsg struct { id string; msg event.Message }`.
  - `func (a *Agent) waiting(ctx context.Context, t *turn) []additionMsg`: the qualifying run.
  - `const maxAdditionsAtOnce = 8`, `const minAdditionTime = 30 * time.Second`.

- [ ] **Step 1: Write the failing tests**, using `newEnv`, `fakeProvider` and the real `sqlqueue`.

```go
func TestAdditionJoinsBeforeTheNextModelCall(t *testing.T)
// script: a tool call, then "answer". The tool puts a message behind the turn while it runs.
// the second request's last user message is the addition, with its speaker line; one reply;
// StateUpdate.Taken holds its id; the reply's ReplyTo is the addition's Ref

func TestWaitingAdditionStopsTheFirstToolCall(t *testing.T)
// the addition is in the inbox before the model answers with two tool calls: neither runs,
// both have a paired result that says it did not run; the next request sees them and the addition

func TestAdditionSkipsTheRestOfABatch(t *testing.T)
// three calls; the addition arrives during the first: the first's result is real, two are skipped

func TestEarlierAnswerGoesOutOnceAsAQuoteAndStaysInHistory(t *testing.T)
// script: "first", then "second". The addition waits when "first" returns.
// Enqueue got one Superseded message "first" under key <idem>:earlier:0;
// the reply is "second"; history is user, "first", addition, "second"

func TestTextBesideSkippedCallsGoesOutAsAQuote(t *testing.T)
// the model writes "checking" with a tool call and an addition waits: the call is skipped and
// "checking" goes out as a quote; beside a call that ran, the text goes out as it does today

func TestRerunDoesNotSendTheQuoteTwice(t *testing.T)
// run the same event twice without completing: the outbox holds one quote

func TestTakingStopsAtTheFirstThatDoesNotQualify(t *testing.T) // Review Focus 1
// table: behind the turn [ok, /new, ok]; [other person, ok]; [press, ok]; [expired, ok]:
// only the leading ok is taken, and a following /new then archives a session with the answer in it

func TestAdditionQualifiesOnlyInAPersonsTurn(t *testing.T)
// a schedule's turn, a press's turn and an expiry's turn take nothing

func TestAdditionOverDailyLimitIsNotTaken(t *testing.T)
// and it sends no alert: the alert belongs to its own turn

func TestForwardedAdditionTaintsBeforeTheNextCall(t *testing.T) // Review Focus 2
// the next tool call's tool.Call.Tainted is true; the text is wrapped; its URL is not known

func TestFileOnlyAdditionIsDescribed(t *testing.T) // Review Focus 2

func TestAdditionDoesNotTouchAParkedCall(t *testing.T) // Review Focus 3
// a call parks; an addition is taken; pressing runs that call once, with its original args

func TestBudgetEndsTheAnswering(t *testing.T)
// MaxSteps 3 and an addition always waiting: three model calls, the last answer is the reply,
// the message behind it stays pending

func TestShortTimeLeftTakesNothing(t *testing.T)
// under minAdditionTime left on the turn's context: nothing taken

func TestFailedTurnKeepsTakenMessages(t *testing.T)
// a model failure after an addition: one failure reply, both messages in history, Taken holds the id

func TestInboxReadFailureDoesNotFailTheTurn(t *testing.T)
```

- [ ] **Step 2: Run:** `go test -race ./agent/ -run 'Addition|Earlier|Rerun|Taking|Budget|ShortTime|FailedTurn|WaitingAddition|FileOnly|Inbox'` Expected: FAIL.

- [ ] **Step 3: Implement `waiting`**

An event qualifies when: `t.event.Kind == KindMessage`, `t.grant == nil`, `!t.noTools`, and for the event: `KindMessage`, same `Conv`, decodes as `payloads` does, `msg.From == t.msg.From`, not `e.Expired(now)`, not a command (mirror `command`: not forwarded and `parseCommand` names one in `a.commands`), and within limits. Split `overLimit` so that the check has no alert: `func (a *Agent) over(ctx, now, principal, eventID) bool`. Stop at the first that fails. Return nothing when `t.record.Steps >= MaxSteps` or less than `minAdditionTime` is left on the context. A read error is logged and means nothing waits.

- [ ] **Step 4: Rewrite the loop in `steps`**

```text
for Steps < MaxSteps:
    additions := waiting()
    for each: content, outside := userContent(m); if outside {Tainted = true}
              t.known gains knownURLs([user]); msgs, added, t.takenUsers += user
              t.taken += id; t.msg.Ref = m.Ref; count a metric
    Steps++
    resp := call(...)
    ...as today...
    if no tool calls:
        if waiting() is not empty:
            enqueue the answer as a quote; msgs, added += the answer; continue
        return the answer
    results := runTools, where before each call: if waiting() is not empty, this and
               the remaining calls get a skipped result and do not run
    if any call was skipped and the model wrote text beside the calls:
        enqueue that text as a quote
```

`added` holds the additions in order, so `converse`'s history line stays as it is; on an `end != OutcomeOK`, `converse` also appends `t.takenUsers`. `finish` puts `t.taken` in `StateUpdate.Taken`. The skipped result's text: `not run: the person sent a new message; decide again`. It is not journaled as a call and has no call number.

The quote: `event.Outgoing{IdemKey: t.event.IdemKey + ":earlier:" + strconv.Itoa(t.quotes), Transport: t.transport, Conv: t.replyConv, Text: text, Superseded: true}` through `Deps.Outbox`. If `Enqueue` fails, log it and append the message to `t.out`, so nothing is lost.

Add to the system prompt: a later message from the same person during work belongs to the same request. Ask the owner about the full-lab recommendation this triggers, per Global Constraints.

- [ ] **Step 5: Add metrics in `agent/telemetry.go`:** additions taken, tool calls skipped, answers sent as quotes; the count of additions on the "agent: turn" log line.

- [ ] **Step 6: Wire `Deps.Inbox` in `config/agent.go`.** Run: `go test -race ./agent/ ./config/` Expected: PASS.

- [ ] **Step 7: Commit**, message `agent: a message written during a turn joins it`, with the code map and level page.

---

### Task 5: Additions on the CLI route

**Files:**
- Modify: `agent/stages.go`, `agent/model.go`, `agent/tools.go`, `agent/turn.go`, `agent/telemetry.go`
- Test: `agent/additions_test.go`, `agent/cli_test.go`, `provider/claudecli/claudecli_test.go`

**Interfaces:**
- Consumes: `waiting` (task 4).
- Produces:
  - `var errAdditionWaiting = errors.New(...)`, the cancel cause of a run ended for an addition, and the error `Exec` returns for a refused call.
  - `const outcomeEnded turns.Outcome`, unexported, never recorded: `call`'s result for a run ended for an addition.
  - On `turn`: `endRun context.CancelCauseFunc`, guarded by `t.mu`.

- [ ] **Step 1: Write the failing tests**

```go
func TestCLIRunEndsAfterAToolWhenAnAdditionWaits(t *testing.T)
// a fake provider that calls Exec, then blocks on ctx. The tool puts an addition behind the turn.
// the run is cancelled with cause errAdditionWaiting; the next request holds the tool call,
// its result and the addition; the turn's outcome is ok

func TestCLICallIsRefusedWhenAnAdditionAlreadyWaits(t *testing.T)
// the addition waits before Exec: the tool does not run, is not numbered and is not journaled

func TestEndedRunIsNotAFailure(t *testing.T)
// no fallback model tried, rec.Error empty, no "failed after running tools", no OutcomeError

func TestEndedRunTokensAreEstimatedIntoTheTurn(t *testing.T)
// the turn's usage grows by the estimate of what was sent

func TestAnswerRacingTheEndIsKept(t *testing.T)
// the provider returns an answer although endRun was called: it goes to the answer boundary

func TestRealRunExitsWhenEnded(t *testing.T) // provider/claudecli
// a fake claude that sleeps; cancelling with a cause ends its process group inside WaitDelay
```

- [ ] **Step 2: Run:** `go test -race ./agent/ ./provider/claudecli/` Expected: FAIL.

- [ ] **Step 3: Implement**

In `steps`, each call runs under `callCtx, end := context.WithCancelCause(turnCtx)`, with `t.endRun = end` under `t.mu`, and `end(nil)` after. In `useTool`, when `capped`: before running, if `waiting()` is not empty, call `t.endRun(errAdditionWaiting)` and return that error, before anything is numbered. After running and logging, check again, and end the run the same way while returning the real result.

In `call`, right after `try` fails and before `logFailure` and the fallback: if `errors.Is(context.Cause(ctx), errAdditionWaiting)`, append an `Attempt` with `Error: "addition"`, add the estimate of what was sent (compaction's estimator) to the usage, and return `outcomeEnded`. In `steps`, `takeExecLog` as today and `continue` on `outcomeEnded`.

- [ ] **Step 4: Add the metric:** runs ended for an addition. Run: `go test -race ./agent/ ./provider/claudecli/ ./config/` Expected: PASS.

- [ ] **Step 5: Commit**, message `agent: a CLI run ends at a tool boundary when an addition waits`.

---

### Task 6: Additions under `kill -9` and in chatlab

**Files:**
- Modify: the crash harness test run by `make crash-agent` (`TestCrashHarnessAgent`, `cmd/`), `tools/chatlab/faults/faults.txt`
- Create: `tools/chatlab/scenarios/mech-addition.yaml`, `tools/chatlab/scenarios/addition-real.yaml`, `tools/chatlab/faults/addition-other-person.patch`, `tools/chatlab/faults/addition-no-taint.patch`

- [ ] **Step 1: Extend the harness.** Some messages arrive while a turn runs. A message counts as answered if a reply carries its Ref, or its `taken_by` names a turn with a reply. Assert in order, one commit per fact, no duplicate quote.

- [ ] **Step 2: Run:** `make crash-agent`, then `GANGLION_CRASH_ITERS=1000 make crash-agent`. Expected: PASS both.

- [ ] **Step 3: Write `mech-addition`,** scripted model: a tool, an addition during it; facts: the journal, one reply. If a scenario step cannot send while the scripted model works, add that step type to chatlab under its existing schema, with a test.

- [ ] **Step 4: Write `addition-real`** with the real model and a judge: the final answer takes both messages into account. `covers:` it narrowly, so that only changes to this mechanism recommend it.

- [ ] **Step 5: Add the two faults** to `faults.txt`. Run: `make lab LAB=mech-addition`, `make lab LAB=addition-real`, and `scripts/lab-faults.sh`. Expected: scenarios green, both new faults red.

- [ ] **Step 6: Measure against the bar again.** Compare an `addition-real` turn's tokens with task 0's numbers. Update the note if they differ.

- [ ] **Step 7: Commit**, message `crash, chatlab: additions`.

---

### Task 7: Status reactions

**Files:**
- Modify: `event/event.go`, `agent/agent.go`, `agent/stages.go`, `agent/tools.go`, `transport/telegram/client.go`, `transport/telegram/transport.go`, `transport/telegram/telegramtest/server.go`, `config/delivery.go`, `config/config.go`
- Create: `transport/telegram/reactions.go`, `transport/telegram/reactions_test.go`

**Interfaces:**
- Produces:
  - `event.Stage` with `StageQueued`, `StageThinking`, `StageTool`, `StageAdded`, `StageDone`; `event.Status{Transport, Conv, Ref string; Stage Stage}`.
  - `agent.Deps.Status func(event.Status)`, with `agent.NoStatus`.
  - `telegram.Reactor`, a component: `NewReactor(c, cfg ReactorConfig, log) *Reactor`, `Run(ctx) error`, `Report(event.Status)` (never blocks, never does I/O), `Noting(next Sink) Sink`.
  - `ReactorConfig{After, Dwell, Linger time.Duration; Now func() time.Time}`; defaults 8 s, 3 s, 800 ms. `After == 0` turns it off.
  - `(*client).setMessageReaction(ctx, chat, msg int64, emoji string) error`; `""` takes it off, sending `"reaction": []`.
  - `telegramtest.Server.Reactions() []Reaction`.

The emoji: queued 👀, thinking 🤔, tool `"\U0001F468‍\U0001F4BB"` (👨‍💻), added 👌. The list is in `ReactionTypeEmoji` of the Bot API, so each is valid.

- [ ] **Step 1: Write the failing tests**, with `Report` and a test-only `step(now)` over a fake clock:

```go
func TestQuickAnswerShowsNothing(t *testing.T)
// Thinking, Done before After: no API call at all

func TestSlowWorkShowsAfterTheThresholdAndComesOff(t *testing.T)
// Thinking; at 8 s 🤔; Done; after the linger, "" once

func TestToolStaysToTheEnd(t *testing.T)
// after a tool: 👨‍💻; a later Thinking changes nothing

func TestDwellHoldsChanges(t *testing.T)
// 🤔 at 8 s; Tool at 9 s: 👨‍💻 not before 11 s

func TestAddedShowsAtOnce(t *testing.T)
// Added at 1 s: 👌 at the next step, with no threshold or dwell

func TestQueuedMessageShowsSeen(t *testing.T)
// StageQueued, no turn: 👀 at 8 s; Thinking then replaces it after the dwell

func TestGroupsShowNothing(t *testing.T)
// a negative chat id: no API call

func TestRefusedReactionDoesNotRepeat(t *testing.T) // Review Focus 4
// 400 on set: logged, counted, not tried again for that message; the next message still works

func Test429PausesAllReactions(t *testing.T) // Review Focus 4
// retry_after 30: no API call for 30 s; the messages' Done still clears them after

func TestStrangerGetsNoReaction(t *testing.T)
// through access.List.Filter(reactor.Noting(put)): a message the list drops reports nothing

func TestResumedTurnShowsTyping(t *testing.T)
// TypingHook on KindDecision sends "typing"; on a "sensor" event it does not
```

- [ ] **Step 2: Run:** `go test -race ./transport/telegram/` Expected: FAIL.

- [ ] **Step 3: Implement `Reactor`.** One goroutine, `Run`, ticking every 200 ms into `step(now)`. Per message: wanted, shown, not-before, done-at. Bound the map: an entry goes when it is cleared, and an entry older than 15 minutes is dropped, so a crash in a turn leaks nothing. Rules are design 4.2. A failed set is logged at debug and counted in `ganglion_telegram_reactions_total{outcome}`. `Noting` reports `StageQueued` after the access filter, by wrapping `q.Put` in `TelegramReceivers`.

- [ ] **Step 4: Report from the agent.** `StageThinking` when `answer` starts; `StageTool` in `applyTool`; `StageAdded` for each taken addition; `StageDone` for `t.msg`'s first Ref and every taken Ref in a defer in `Handle`, on every path, using the Ref each message had. Only for a person's message.

- [ ] **Step 5: Wire and configure.** `Reactor` as a core component in `config/`, settings `GANGLION_REACTION_AFTER` and `GANGLION_REACTION_DWELL` beside the node's other settings. `TypingHook` also runs for `KindDecision`. Add `setMessageReaction` to the fake server.

- [ ] **Step 6: Run:** `go test -race ./transport/telegram/ ./agent/ ./config/` Expected: PASS.

- [ ] **Step 7: Commit**, message `telegram: reactions show work that takes long`, with the code map and level page.

---

### Task 8: The plan's store and tools

**Files:**
- Create: `plan/plan.go`, `plan/plan_test.go`, `plan/migrations/001_plans.sql`, `tool/builtin/plan.go`, `tool/builtin/plan_test.go`
- Modify: `agent/tools.go`, `agent/agent.go`, `agent/stages.go`, `config/agent.go`

**Interfaces:**
- Produces:
  - `plan.Status` with `Waiting`, `Running`, `Done`, `Failed`, `Skipped`, shown as `·`, ⏳, ✅, ❌, ⏭.
  - `plan.Render(p Plan) string`, pure.
  - `plan.Store`: `Show(ctx, c tool.Call, steps []string) error`, `Step(ctx, conv string, n int, st Status, note, outcome string) error`, `Finish(ctx, conv string, kept bool) error`.
  - Tools `plan.show` and `plan.step`: `Class: tool.ClassRead`, `Policy: tool.PolicyAuto`, `AdminOnly: true`, `LocalSafe: false`. A local-only session has no plan, since the message leaves the node.
  - `agent.Deps.Plans interface{ Finish(ctx, conv string, kept bool) error }`, with `NoPlans`.

Table `plans`: `conv` primary key, `idem_key`, `steps` (JSON), `version`, `active`, `dirty`, `last_edit_ms`, `updated_ms`. One active plan per conversation, written when shown, in its own transaction.

Limits: 1 to 20 steps; each on one line, at most 200 runes; a step's note at most 200; the whole rendered text at most 3,500 UTF-16 units. A call past these returns an error for the model. An active plan untouched for 1 hour counts as closed (the M3d design's TTL).

- [ ] **Step 1: Write the failing tests**

```go
func TestShowEnqueuesOneMessageAtOnce(t *testing.T)
// the outbox has a message under "plan:"+c.Key()+":show" before the turn ends; no buttons

func TestSecondShowEditsTheSameMessage(t *testing.T)
// an active plan: no second new message; the next edit has the new steps

func TestRerunFindsTheActivePlan(t *testing.T)
// Show with another call key while a plan is active: no new message

func TestStepEditsThrottledAndCoalesced(t *testing.T)
// fake clock: three steps within 2 s make one edit with all three, under a key with the version

func TestStepMarksTheNextRunning(t *testing.T)
// Step(1, Done): line 1 ✅, line 2 ⏳

func TestFinishFlushesAndClosesUnfinishedLines(t *testing.T)
// Finish(kept=false): ⏳ and · become ⏭; the plan is inactive; a final edit is enqueued

func TestFinishKeepsAParkedPlan(t *testing.T)
// Finish(kept=true): lines unchanged, still active; a resumed turn's Step finds it

func TestEditWaitsForTheMessageRef(t *testing.T)
// SentRef "" at first: Finish waits (injected after) up to 5 s; still "" leaves it dirty, with no error

func TestLimitsAreRefused(t *testing.T)
// table: 0 steps, 21 steps, a 201-rune step, a newline in a step (joined), an unknown n in Step

func TestPlanCallsDoNotSpendTheCap(t *testing.T) // agent
// a CLI turn with 8 real calls and plan calls between them: every real call runs

func TestPlanCallsHaveTheirOwnCeiling(t *testing.T) // agent
// the 33rd plan call in a turn returns errCallLimit

func TestPlanInjectedTextIsNotMarkup(t *testing.T)
// a step with Markdown is rendered as the model's other text is, and no button appears
```

- [ ] **Step 2: Run:** `go test -race ./plan/ ./tool/builtin/ ./agent/` Expected: FAIL.

- [ ] **Step 3: Implement.** The edit is `event.Outgoing{Edit: ref, Text: Render(p), IdemKey: "plan:" + idem + ":v" + version}` through `Outbox.Enqueue`; `SentRef(idem)` finds `ref`. Edits at most once per 2 s; otherwise `dirty` stays for the next step or `Finish`. In `useTool`, plan tools skip the `MaxSteps` cap and count in `t.planCalls`, with a ceiling of 32. `finish` calls `Plans.Finish(rctx, conv, len(t.state.Pending) > 0)`, bounded by its 5 s context, and a failure is logged only.

- [ ] **Step 4: Add metrics:** plans shown, edits sent, edits coalesced. Run: `go test -race ./plan/ ./tool/builtin/ ./agent/ ./config/` Expected: PASS.

- [ ] **Step 5: Commit**, message `plan: a plan shown during the turn and edited as it goes`, with the code map.

---

### Task 9: The directive, acceptance and closing

**Files:**
- Modify: `agent/prompt.go`, `agent/prompt_test.go`, `docs/README.md`, `docs/plans/p1-all-in-one.md`, `docs/architecture/05-code-map.md`, the matching level page
- Create: `tools/chatlab/scenarios/plan-shown.yaml`

- [ ] **Step 1: Add the prompt's directive** (layer 2): before work with several steps, call `plan.show`; send `plan.step` beside the next real call in the same message; write the outcome in the last one; no plan for one quick step. Test its presence in `prompt_test.go`. Ask the owner about the full-lab recommendation.

- [ ] **Step 2: Write `plan-shown`** with the real model: a task with three steps shows one plan message with edits before the final reply. Run: `make lab LAB=plan-shown`. Expected: pass; the edits arrive before the reply.

- [ ] **Step 3: Run the checks:** `make lint`, `go test -race ./...`, `make accept`, `make memory`. Expected: all pass; record RSS and binary size against M3c's (44 MB RSS after 1,000 turns, 47.7 MB binary).

- [ ] **Step 4: The owner's live check on @ganglion_test_bot** (`make live-agent`), since chatlab cannot see real Telegram. Say in advance why it is manual. To try:
- a question, then an addition while a web search runs: 👌, one final answer;
- an addition while the model writes: a collapsed quote, then the answer;
- short questions: no reaction and no buzz; is 8 s right? Tune it through `GANGLION_REACTION_AFTER`;
- a task with several steps: does the plan read well, does it flicker?

- [ ] **Step 5: Record the owner's numbers.** Update the reaction defaults if changed, and the ADRs' text if a decision changed.

- [ ] **Step 6: Close the documents.** "Where we are" in `docs/README.md`, ticks in the phase plan and here.

- [ ] **Step 7: Commit**, message `docs: live turns done`.
