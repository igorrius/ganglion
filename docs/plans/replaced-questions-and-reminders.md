# Replaced questions and reminders without a press — implementation plan

Issues: [#214](https://github.com/igorrius/ganglion/issues/214),
[#215](https://github.com/igorrius/ganglion/issues/215). Decisions:
[ADR-0088](../adr/0088-a-newer-question-replaces-an-older-one-of-the-same-tool.md),
[ADR-0089](../adr/0089-set-a-reminder-without-a-press-in-a-clean-session.md);
spec R-POL-9, R-SCH-2. Read [conventions](../conventions.md) first.

## A. A newer question replaces a waiting one (#214)

- [ ] **A1. Migration** `tool/pending/migrations/008_replaced.sql`:
  `ALTER TABLE pending_calls ADD COLUMN replaced_by TEXT NOT NULL DEFAULT '';`
  with a comment in the style of `007_job.sql`.
- [ ] **A2. Store** (`tool/pending`):
  - `StatusReplaced Status = "replaced"`; `ErrReplaced` beside `ErrExpired`;
    a `spentReplaced = "spent:replaced"` used_by beside `spentBy`.
  - `func (s *Store) Replace(ctx, id string) ([]Item, error)`: in one
    `s.tx`, read the new call's tool, principal, conv, turn_id, created_ms;
    set `status = 'replaced', replaced_by = id, answered_ms = now` on every
    call with `status = 'waiting'`, the same tool, principal and conv,
    `turn_id <> new turn_id`, created before the new one (`created_ms <`
    or equal with `id <`); spend their unused buttons (`used = 1, used_by
    = spentReplaced`); return every call `WHERE replaced_by = id` ordered
    by `created_ms, id` (so a rerun gets the same set). After the
    transaction, answer each one's request cell no (`s.cells.Answer(ctx,
    cellID(it.ID), false, "replaced")`), ignoring `cells.ErrDecided` and
    `cells.ErrNotFound` as `answerCell` does. Unknown id: `ErrNotFound`.
  - `refused`: a button whose call has `StatusReplaced` returns the press
    with the item and `ErrReplaced` (after the "same event pressed it"
    case, before the used/expired cases).
  - `ExpireDue` must not touch replaced calls (it already selects
    `waiting`; add a test).
- [ ] **A3. Agent** (`agent`):
  - Add `Replace` to the `Confirmations` interface (`agent/agent.go`) and
    to every fake that implements it.
  - In `applyTool` (`agent/tools.go`), when `result.Pending != ""`, call
    `a.d.Pending.Replace(ctx, result.Pending)`. For each replaced item:
    remove its id from `t.state.Pending`; fold its question with a new
    `closeReplaced` modelled on `closeExpired` (`agent/resume.go`): find
    the sent ref by `it.Call().Key()+pending.QuestionKeySuffix`; when
    found, append an edit `event.Outgoing{IdemKey: it.Call().Key() +
    ":replaced", Transport: it.Transport, Conv: it.Conv, Text:
    a.stamped(a.say(ctx, t, texts.LineReplaced, nil), it.Answered),
    Folded: it.Question, Edit: ref}`. The key is the replaced call's, so a
    rerun or a later `Parked` hit stores the edit once. When any were
    replaced, append to the text the model reads: " It replaced the
    earlier waiting question of the same tool, which the owner can no
    longer press." A failed `Replace` is logged and does not fail the
    call (the old question then expires as today).
  - In `pressed` (`agent/resume.go`): `case errors.Is(err,
    pending.ErrReplaced)`: toast `texts.ToastReplaced`, and
    `t.closeButtons(p.Item.Question, a.stamped(a.say(ctx, t,
    texts.LineReplaced, nil), p.Item.Answered))`.
- [ ] **A4. Texts**: `ToastReplaced` ("Replaced by a newer question.") and
  `LineReplaced` ("Replaced by a newer question"; `stamped` adds the
  time, check how `LineExpired` reads) in `texts/texts.go`, its lists, and
  `texts/lang/en.yaml`, `uk.yaml`, `ru.yaml`, worded like their neighbours.
- [ ] **A5. Tests**: store — replaced: same tool from another turn;
  not replaced: the same turn, another tool, another principal, another
  conv, an answered call; a second `Replace` returns the same set; a press
  on a replaced button gives `ErrReplaced`; `ExpireDue` leaves replaced
  calls. Agent — a second turn's question folds the first (the edit with
  the line and `Folded`), drops it from the session's pending list, and a
  press on the old Approve runs nothing and toasts.

## B. A reminder without a press in a clean session (#215)

- [ ] **B1. Policy** (`policy/policy.go`): a rule `ruleReminder` in
  `rules`, right after `ruleMemoryWrite`: for `schedule.create` only;
  decode the args loosely (`kind`, and whether `grant` is present and not
  null); if they do not decode, or `kind != "reminder"`, or a grant is
  given, the rule does not apply (the side-effect rule then confirms).
  Clean session: auto (`journal.DecisionAllowed`); tainted: confirm.
  Update the numbered list in `Decide`'s doc comment.
- [ ] **B2. Tool** (`tool/builtin/schedule.go`): `ScheduleCreate.Run`, for a
  reminder, returns an `Out` message `IdemKey: c.Key()+":created"`, to
  `c.Transport`/`c.Conv`, text from a new key `texts.SchedReminderSet`
  ("⏰ Reminder set: {{timing}}" followed by the reminder's text, built the
  way `entry` shows a job's text), and one Delete button from
  `s.buttons(ctx, c.Speak, j, c.Principal, c.Conv, ActionDelete)`. The
  result text for the model adds: "The owner sees it as a message with a
  Delete button; confirm in a few words without repeating it." Update the
  tool description: a reminder is set at once in a clean session; the
  owner confirms a task with its grant. Update `Spec`'s doc comment.
- [ ] **B3. Texts**: `SchedReminderSet` in `texts.go`, its list and the
  three language files.
- [ ] **B4. Tests**: `TestDecide` cases — reminder clean: auto; reminder
  tainted: confirm; reminder with a grant: confirm; task clean: confirm;
  args that do not parse: confirm. Builtin — `Run` of a reminder returns
  the message with one stored Delete button; a task returns none. Find
  tests and chatlab scenarios that expect a question for a reminder
  (`grep -rn -i remind --include=*_test.go`, `internal/chatlab`) and move
  them to the new behaviour; keep one that checks the tainted case asks.

## C. Docs

- [ ] `docs/guide/using/memory.md` (the paragraph "You confirm each one when
  it is created"): a reminder is set at once and shown with Delete; in a
  tainted session it asks; a task is confirmed as before.
- [ ] `docs/guide/using/tools-and-safety.md`, "Confirmation buttons": the
  exceptions (a reminder in a clean session) and that a question asked
  again in a later message replaces the old one, which folds.
- [ ] `docs/architecture/05-code-map.md` if it lists pending statuses or
  policy rules (grep); nothing else in architecture changes.

## Check

`go build ./...`, `go vet ./...`, `go test ./...`, and the repo's linter if
`Makefile` or CI names one. Every task above ticked.
