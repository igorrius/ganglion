# P1 · Fixed texts in the person's language — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.
>
> **Execution (owner, 2026-11-06):** hybrid, in the session that wrote the plan. Tasks are
> implemented inline; a fresh reviewer, given the task's diff and its section of this plan,
> checks tasks 5, 6 and 7, and one more reviews the whole branch at the end.

**Goal:** everything the node writes to a person without a model goes out in that person's
language: Telegram's unless they chose another by saying so in the chat, and a language with
no file gets one translated once by a model and marked for the reader.

**Architecture:** a new package `texts` holds the catalogue, its checks and the table of
native names, with no I/O beyond embedded files. A new package `language` holds each person's
language and the machine packs in SQLite, and translates in the background. The agent resolves
a language at the start of a turn; tools get it on `tool.Call`.

**Tech stack:** Go 1.27, SQLite through `sqlitedb`, `go.yaml.in/yaml/v3` (already a
dependency). No new dependency.

**Spec:** [p1-fixed-texts-language-design.md](p1-fixed-texts-language-design.md), approved by
the owner on 2026-11-06. Read it with this plan.

## Global Constraints

- All documentation, comments and doc comments are in English; [conventions](../conventions.md) apply.
- Spec first, then code. Architecture docs change in the same commit as the code they describe
  (`docs/architecture/05-code-map.md`, the matching level page).
- A metric and a log line come in the same commit as what can fail, wait, retry or cost
  (R-OBS-8). Labels are low-cardinality: a language code from the table is fine; a principal,
  a conversation or message text never.
- ADRs stay Proposed until the owner accepts them.
- No new Go dependency. No `time.Sleep` in tests: inject clocks.
- A full chatlab run is never automatic: use `make lab` for what changed and ask the owner
  before `make lab-all` (about 900,000 tokens).
- Commit only with the owner's go-ahead for this plan's execution; never on `master`.
- Fixed texts cost no model tokens per message. The only per-turn additions are the tool's
  description and, for a person who chose, one line of notes.

## Review Focus

Failure modes the design implies and no task would test by itself. Each has its test in the
named task.

1. **A garbage `language_code`** (empty, 1,000 characters, `zh-hans`, `xx`): it never replaces
   a language already known, and never starts a translation (task 3).
2. **A person's language changes while a question is open:** the stored question stays as it
   was asked, and the folded line and toast use the language of the person at the press (task 5).
3. **A rerun after `kill -9`:** `language.set` under the same call key changes nothing twice and
   the reply is not doubled (task 6).
4. **A model that echoes English or translates placeholders:** a pack whose texts all equal
   English, or `{name}` turned into `{имя}`, is refused, and the node stays on English (task 7).
5. **A release that changes one English text:** only that key is translated again, and the
   others keep their translation (task 7).

---

### Task 1: The decision on paper

**Files:**
- Create: `docs/adr/0053-fixed-texts-in-the-persons-language.md`
- Modify: `docs/adr/README.md`, `docs/spec.md`

- [x] **Step 1: Write ADR-0053, Proposed**, from `docs/adr/template.md`. Compare at least: A)
  a catalogue with shipped files only; B) a catalogue with a one-time machine translation in a
  clean call (chosen); C) the model wording fixed texts on every message. Take the Security
  section from design section 8. State that it replaces ruling R46, and name the native-name
  table's list. The tool description's token cost is added in task 6.
- [x] **Step 2: Add the row** to `docs/adr/README.md`.
- [x] **Step 3: Add spec section `### 7.13. The language of fixed texts`** before `## 8.`:
  - **R-LANG-1:** the order: chosen, last seen from Telegram, English.
  - **R-LANG-2:** which texts follow it, and the diagnostics that stay in English.
  - **R-LANG-3:** choosing in the chat through `language.set`, at once in a clean session,
    and with a question in both languages in a tainted one.
  - **R-LANG-4:** priority: an operator's file, a shipped file, a machine translation.
  - **R-LANG-5:** the machine translation: clean call, the checks, per-key hashes.
  - **R-LANG-6:** the once-only notice, and symbols added by code on buttons that act.
- [x] **Step 4: Update the "Not now" list:** remove "Fixed texts in the person's language";
  mark ruling R46 in its entry as replaced by R-LANG.
- [x] **Step 5: Commit** `docs: fixed texts follow the person's language (ADR-0053, Proposed)`.

### Task 2: Package `texts`, the catalogue

**Files:**
- Create: `texts/texts.go`, `texts/native.go`, `texts/check.go`, `texts/texts_test.go`,
  `texts/lang/en.yaml`, `texts/lang/ru.yaml`, `texts/lang/uk.yaml`
- Modify: `docs/architecture/05-code-map.md`

**Interfaces:**
- Produces:
  - `type Lang string`; `const English Lang = "en"`
  - `type Key string`, with one constant per text (a closed set, conventions section 3)
  - `type Source string`: `SourceOperator`, `SourceShipped`, `SourceMachine`, `SourceEnglish`
  - `type Vars map[string]string`
  - `func Normalize(code string) (Lang, bool)`: the primary subtag, lowercased; false if not in the table
  - `func Native(l Lang) string`: the language's name in itself
  - `func New(opts ...Option) (*Catalog, error)`; `func WithFiles(map[Lang]map[Key]string) Option`,
    `func WithPacks(PackFunc) Option`, where `type PackFunc func(Lang, Key) (string, bool)`
  - `func (c *Catalog) Text(l Lang, k Key, v Vars) string`: for a message; the template is
    escaped for Markdown before substitution, values are not
  - `func (c *Catalog) Label(l Lang, k Key, v Vars) string`: for a button or toast; nothing escaped
  - `func (c *Catalog) Source(l Lang, k Key) Source`
  - `func (c *Catalog) Has(l Lang) bool`: every approval text is present from a file or pack
  - `func Keys() []Key`, `func EnglishText(k Key) string`, `func IsApproval(k Key) bool`
  - `func Check(en, tr map[Key]string) (ok map[Key]string, rejected map[Key]string)`: the checks,
    with a reason per rejected key

- [ ] **Step 1: Write the failing tests** in `texts/texts_test.go`:
  - `TestShippedFilesMatchEnglish`: for `ru` and `uk`, the key sets are equal and each
    text's placeholders equal English's.
  - `TestLimits`: every `Label` text within 200 characters for a toast, 32 for a button.
  - `TestFallsBackPerKey`: a pack with one key gives that key, and English for the others.
  - `TestPriority`: operator over shipped over machine.
  - `TestNormalize`: `pt-BR` → `pt`, `UK` → `uk`, `""`, `xx` and a 1,000-character string → false.
  - `TestTextEscapesTemplateNotValues`: template `*{a}*` with `a="**b**"` escapes the template's
    stars and leaves the value's; `Label` leaves both.
  - `TestCheck`: a dropped placeholder, an added one, an added URL, an added `@mention`, added
    Markdown, an over-limit text, and an extra key are each rejected; a clean text passes.
- [ ] **Step 2: Run** `go test ./texts/`. Expected: FAIL, no package.
- [ ] **Step 3: Implement.** Embed `lang/*.yaml`. The inventory of keys grows with tasks 4 and 5;
  start with the keys those tasks use first. The native table is the design's few dozen languages,
  as listed in ADR-0053. Add the code-map row.
- [ ] **Step 4: Run** `go test ./texts/`. Expected: PASS.
- [ ] **Step 5: Commit** `texts: a catalogue of fixed texts with per-key fallback and checks`.

### Task 3: A person's language

**Files:**
- Create: `language/language.go`, `language/migrations/001_language.sql`, `language/language_test.go`
- Modify: `event/message.go`, `transport/telegram/types.go`, `transport/telegram/transport.go`,
  `transport/telegram/buttons.go`, `agent/stages.go`, `agent/agent.go`, `config/agent.go`,
  `docs/architecture/05-code-map.md`

**Interfaces:**
- Consumes: `texts.Normalize`.
- Produces:
  - `event.Message.Lang`, `event.Decision.Lang` (`string`, JSON `lang`, omitted when empty), raw from the channel
  - `language.New(ctx, db, opts...) (*Store, error)`
  - `func (s *Store) Seen(ctx, principal, code string) error`: writes only if valid and changed
  - `func (s *Store) Resolve(ctx, principal string) (texts.Lang, error)`: chosen, then seen, then English
  - `func (s *Store) Chosen(ctx, principal string) (texts.Lang, bool, error)`
  - `func (s *Store) Choose(ctx, principal string, l texts.Lang) (changed bool, err error)`
  - `func (s *Store) Wanted(ctx) ([]texts.Lang, error)`: distinct languages of all persons
  - `turn.lang texts.Lang`, set in the stage after `setAdmin`; `agent.Deps.Languages` (an interface in `agent`)

- [ ] **Step 1: Write the failing tests:**
  - `TestResolveOrder`: all three steps.
  - `TestSeenIgnoresGarbage`: after `Seen(uk)`, then `Seen("")`, `Seen("xx")` and a 1,000-character
    string, `Resolve` is still `uk` (Review Focus 1).
  - `TestSeenDoesNotOverrideChosen`.
  - `TestSeenWritesOnlyOnChange`, by counting rows' `updated_ms` under an injected clock.
  - In `transport/telegram`: `TestLanguageCodeReachesTheEvent`, for a message and a press.
  - In `agent`: `TestTurnResolvesLanguage`; a scheduled turn gets its job owner's language; a
    message to a group with no person behind it is in English.
- [ ] **Step 2: Run** `go test ./language/ ./transport/telegram/ ./agent/`. Expected: FAIL.
- [ ] **Step 3: Implement.** `user` gains `LanguageCode string` with the JSON name `language_code`.
  The table: `principal` primary key, `chosen`, `seen`, `notified` (a comma-separated list of
  languages), `updated_ms`. A store error while resolving logs a warning and gives English: a
  message is never lost over a language. Add the code-map row.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `language, telegram, agent: each person has a language`.

### Task 4: The agent's texts move

**Files:**
- Modify: `agent/ack.go`, `agent/commands.go`, `agent/prompt.go`, `agent/resume.go`,
  `agent/scheduled.go`, `agent/agent.go`, `config/agent.go`, `texts/texts.go`, `texts/lang/*.yaml`

**Interfaces:**
- Consumes: `texts.Catalog.Text`, `Label`; `turn.lang`.
- Produces: `agent.Deps.Texts *texts.Catalog`.

- [x] **Step 1: Write down the inventory** in this plan, under this task: each string a person
  sees, with its file and key. *Done: `agent/ack.go` toasts and lines (`toast.*`, `line.running`,
  `line.approved`, `line.approved_failed`); `agent/resume.go` lines (`line.declined`, `line.used`,
  `line.expired`); `agent/prompt.go` and `agent/limits.go` (`reply.no_model`, `reply.step_limit`,
  `reply.expired`, `reply.daily_limit`, `reply.give_up`); `agent/commands.go`, `agent/last.go`,
  `agent/metrics.go` and `agent/modules.go` (`reply.new_session`, `reply.no_sessions`,
  `reply.no_such_resume`, `reply.resumed`, `reply.session_row`, `reply.no_tasks`,
  `reply.not_available`). Task's answer text in `agent/scheduled.go` is a model's or the job's own.* Leave out what a model reads (prompts, `errorPrefix`, the speaker
  and task lines) and the diagnostics in the design's non-goals (`agent/last.go`, `agent/metrics.go`,
  `agent/modules.go`, admin alerts).
- [ ] **Step 2: Write the failing tests:**
  - `TestRepliesFollowTheLanguage`: `/new`, `/sessions` empty and the daily limit, for a person
    in `uk` and one in `en`, through a scripted model.
  - `TestToastAndLineUseThePressersLanguage`.
  - `TestRepliesWordedWithoutPlurals`: no English text has a count before a noun. This is a table
    the inventory fills, not a language rule.
- [ ] **Step 3: Run** `go test ./agent/`. Expected: FAIL.
- [ ] **Step 4: Move the strings** to keys, with Russian and Ukrainian in the files; reword any
  count as "Tasks: 3". English wording otherwise stays as it is, so existing tests change only
  where a count is reworded.
- [ ] **Step 5: Run** `make test`. Expected: PASS; `TestShippedFilesMatchEnglish` passes with all keys.
- [ ] **Step 6: Commit** `agent: fixed replies, toasts and lines follow the person's language`.

### Task 5: The tools' texts and buttons move

Reviewer's gate: approvals change here.

**Files:**
- Modify: `tool/tool.go`, `agent/tools.go`, `tool/pending/pending.go`, `tool/builtin/question.go`,
  `tool/builtin/message.go`, `tool/builtin/persona.go`, `tool/builtin/schedule.go`,
  `tool/builtin/schedule_quick.go`, `tool/builtin/web.go`, `tool/builtin/skill.go`,
  `tool/builtin/cells.go`, `tool/builtin/memory.go`, `texts/texts.go`, `texts/lang/*.yaml`

**Interfaces:**
- Consumes: `turn.lang`, `texts.Catalog`.
- Produces: `tool.Call.Lang texts.Lang`; `pending.Item.Lang texts.Lang`, which sets the labels
  at the moment the buttons are stored.

- [ ] **Step 1: Write the failing tests:**
  - `TestButtonsCarrySymbolsFromCode`: for `en`, `uk` and a pack whose labels are nonsense, the
    labels start with ✅ and ❌; `Delete` in the task list starts with 🗑.
  - `TestQuestionShowsItsSubjectAsIs`: for `web.fetch`, the address, and for `message.send`, the
    text, appear byte for byte in a `uk` question, and a Markdown-looking address stays as it was.
  - `TestOpenQuestionKeepsItsText` (Review Focus 2): a question stored in `en`, the person then
    choosing `uk`: the question's stored text is unchanged, and the press's line and toast are in `uk`.
  - `TestRerunKeepsTheSameQuestion`: the same call key gives the same text after a language change,
    since `pending` keeps the first row (R-DLV-6).
- [ ] **Step 2: Run** `go test ./tool/... ./agent/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Symbols are constants in `pending` and `schedule_quick.go`, joined
  before the label. A tool's error text and result stay in English, since a model reads them.
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `tool: questions and buttons follow the person's language; symbols come from code`.

### Task 6: `language.set`

Reviewer's gate: a security boundary and a tool offered to `family`.

**Files:**
- Create: `tool/builtin/language.go`, `tool/builtin/language_test.go`,
  `tools/chatlab/scenarios/language-switch.yaml`
- Modify: `config/agent.go`, `agent/stages.go`, `agent/prompt.go`, `docs/adr/0053-fixed-texts-in-the-persons-language.md`

**Interfaces:**
- Consumes: `language.Store.Choose`, `texts.Normalize`, `texts.Native`, `tool.Call.Lang`.
- Produces: `builtin.NewLanguageSet(LanguageChooser, *texts.Catalog, opts...) *LanguageSet`, with
  `Spec()` as: `Name: "language.set"`, `Class: tool.ClassSideEffect`, `Policy: tool.PolicyAuto`,
  `AdminOnly: false`, `LocalSafe: true`, `AskTaintedInput: true`.

- [ ] **Step 1: Write the failing tests:**
  - `TestLanguageSetClean`: applied at once, no question; the next turn's fixed text is in the new language.
  - `TestLanguageSetTainted`: `PolicyConfirm`; the question has the requested language's line, then
    the current one's; approved, it applies; denied, nothing changes.
  - `TestLanguageSetSameTextOneLine`: both languages give the same text, so one line.
  - `TestLanguageSetNoFile`: `pl` with no file gives the English question with `Polski`.
  - `TestLanguageSetTakesNoTarget`: the schema rejects any argument but `code`, and the row changed
    is `c.Principal`'s.
  - `TestLanguageSetUnknownCode`: an error result, nothing written.
  - `TestLanguageSetFamily`: offered and works for a `family` member; `Registry.Specs(false)` lists only this tool.
  - `TestLanguageSetNotInScheduledRun`: refused when `c.Grant != nil`.
  - `TestLanguageSetRerun` (Review Focus 3): the same call key after a restart gives no second change.
  - In `policy`, a table case that `ruleSideEffect` does not turn this tool's clean call into a question.
    If it does, stop and report: the design's "at once" needs a policy change, reviewed on its own.
- [ ] **Step 2: Run** `go test ./tool/builtin/ ./policy/ ./agent/`. Expected: FAIL.
- [ ] **Step 3: Implement.** The description says it is for a lasting choice, not for translating
  one text. Register it in `config/agent.go`. A chosen language adds `[write to this person in <English
  name of language>]` to `turnLines` in `agent/stages.go`. Add a line to `IntroductionRules` to call
  the tool once the language is known. Metrics: `language.set` calls by outcome, and languages
  asked for, by how they were chosen.
- [ ] **Step 4: Run** `make test`. Expected: PASS. Update every test that assumed a family member
  has no tools at all, and name them in the task's report.
- [ ] **Step 5: Add the chatlab scenario:** "пиши польською" chooses `pl`; "переклади це польською:
  …" does not call the tool. Run it: `LAB=language-switch make lab`. Expected: n of n passes.
- [ ] **Step 6: Measure** the tokens the tool's description adds to one turn, and write the number
  in ADR-0053.
- [ ] **Step 7: Commit** `builtin: language.set lets a person choose by saying so`.

### Task 7: Machine translation

Reviewer's gate: a model's output becomes text on buttons.

**Files:**
- Create: `language/translate.go`, `language/translate_test.go`, `language/migrations/002_packs.sql`
- Modify: `router/router.go`, `config/agent.go`, `config/memory.go` or a new `config/language.go`,
  `docs/architecture/05-code-map.md`, `docs/architecture/02-containers.md` if a connection changes

**Interfaces:**
- Consumes: `router.Router`, `map[string]provider.Provider`, `texts.Keys`, `EnglishText`, `Check`, `Native`.
- Produces:
  - `router.TaskTranslate` (a new `Kind`, routed at `TierCheap`, as `guard.check` does)
  - `func (s *Store) Pack(l texts.Lang, k texts.Key) (string, bool)`, matching `texts.PackFunc`,
    read from memory and refreshed after each translation
  - `language.NewTranslator(*Store, router.Router, map[string]provider.Provider, opts...) *Translator`
  - `func (t *Translator) Run(ctx) error`: a node component; `func (t *Translator) Wake()`, non-blocking

- [ ] **Step 1: Write the failing tests**, with a scripted provider and a fake clock:
  - `TestTranslateOncePerLanguage`: two persons in `pl` make one call, and later turns none.
  - `TestStaleKeyOnly` (Review Focus 5): a changed English hash on one key sends only that key.
  - `TestBadOutputRefused`, as a table:
    - not JSON;
    - a missing approval key refuses the whole pack;
    - a dropped placeholder keeps that key in English;
    - `{имя}` for `{name}` likewise (Review Focus 4);
    - an added URL likewise;
    - a pack whose texts all equal English is refused.
  - `TestRequestHoldsOnlyOurStrings`: the request has a system prompt and one user message of our
    JSON, no tools and no history.
  - `TestDailyCap`: a failing model is called no more than the cap per day, and English stays.
  - `TestRestartResumes`: a fresh `Translator` over the same database translates what is still
    wanted, and does nothing when it is done.
  - `TestNeverHoldsATurn`: `Wake` returns at once while a call hangs.
  - `TestUnlistedLanguageNeverTranslated`.
  - `TestTaintedRequestWaitsForThePress`: a question open for `pl` starts no translation until it is approved.
- [ ] **Step 2: Run** `go test ./language/ ./router/`. Expected: FAIL.
- [ ] **Step 3: Implement.** The work is derived, so nothing needs a queue: on `Wake` and on an
  hourly tick, for each language in `Wanted` with no file, send the keys that are missing or stale.
  Write accepted texts in one transaction. The cap is 3 attempts per language per day. Metrics
  (design section 9): runs by outcome, tokens, cost, keys refused. `Store.Choose` and `Seen` call
  `Wake` through a callback when the language is new. Register the component in `config`.
- [ ] **Step 4: Run** `go test -race ./language/ ./router/`. Expected: PASS.
- [ ] **Step 5: Commit** `language: a language with no file is translated once, in a clean call`.

### Task 8: Operator files, the notice, command hints

**Files:**
- Modify: `config/bootstrap.go`, `config/agent.go`, `texts/texts.go`, `agent/stages.go`,
  `agent/agent.go`, `language/language.go`, `transport/telegram/commands.go`, `docs/README.md`
  (a "Language files" how-to)

**Interfaces:**
- Consumes: `texts.WithFiles`, `Catalog.Source`, `texts.Check`.
- Produces: `Bootstrap.LanguagesDir` (`GANGLION_LANGUAGES`, default `languages/` beside the database);
  `func (s *Store) NoticeDue(ctx, principal string, l texts.Lang) (bool, error)`; `MarkNotice`, in the
  same transaction as the outbox row; `Transport.SetHints(ctx, lang, cmds)`.

- [ ] **Step 1: Write the failing tests:**
  - `TestOperatorFileLoadsAndWins`; `TestOperatorFileBadKeyRejectedWithLog`: a bad text stays in
    English and the node starts.
  - `TestOperatorDirOutsideWorkspace`: a `GANGLION_LANGUAGES` inside the workspace fails at start.
  - `TestNoticeOnce`: machine `pl`, two replies, so one notice, with its text in `pl`.
  - `TestNoticeNotForShipped`: `uk` gets none.
  - `TestNoticeEnglishIfItsKeyFailed`.
  - `TestNoticeSurvivesRerun`: a rerun of the turn after `kill -9` does not send it twice.
  - `TestHintsSetWhenPackReady`: `setMyCommands` with `language_code=pl` once.
- [ ] **Step 2: Run** `go test ./config/ ./texts/ ./agent/ ./transport/telegram/`. Expected: FAIL.
- [ ] **Step 3: Implement.** The notice is appended to the outgoing text as its last paragraph,
  using `Catalog.Text`, so no second message goes out. Metrics: texts sent by language and source.
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `language, agent: the person is told once; an operator's file comes first`.

### Task 8a: Regular reports follow the owner's language (R-LANG-7, owner, 2026-11-06)

**Files:**
- Modify: `agent/agent.go`, `agent/scheduled.go`, `tool/builtin/memory.go`, `memory/audit.go`,
  `config/memory.go`, `texts/texts.go`, `texts/lang/*.yaml`

**Interfaces:**
- Produces: `agent.SystemJob` becomes `func(ctx context.Context, sp texts.Speaker) (string, error)`;
  `memory.Report` keeps its counts and findings, and its wording moves to `builtin` under keys. The
  `memory.audit` tool's result stays in English, since a model reads it.

- [ ] **Step 1: Write the failing tests:**
  - `TestAuditReportFollowsTheOwnersLanguage`: a `uk` owner gets the title and the counts in Ukrainian,
    with nothing flagged and with one finding; a finding's path and reason stay byte for byte.
  - `TestAuditToolResultStaysEnglish`.
  - `TestSystemJobGetsTheOwnersSpeaker`, through `agent.system`.
- [ ] **Step 2: Run** `go test ./agent/ ./tool/builtin/ ./memory/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Keys `report.audit_clean` and `report.audit_flagged`, worded with no plural:
  "Facts checked: {facts}. Persona files checked: {persona}."
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `agent, builtin: a regular report follows its owner's language`.

### Task 9: Close

**Files:** Modify `docs/README.md`, this plan.

- [ ] **Step 1: Measure** with `make memory`, and record the binary and RSS against 48.0 MB and 43.9 MB.
- [ ] **Step 2: Run** `make test`, `make lint`, and `make lab` for what changed. Ask the owner
  before any full run.
- [ ] **Step 3: Request a whole-branch review** with superpowers:requesting-code-review.
- [ ] **Step 4: The owner's look on the test bot:**
  - a Telegram set to Ukrainian, then to English;
  - "write in Russian" spoken in a chat;
  - a language with no file, reading the notice;
  - after reading a page, the question in two languages.
- [ ] **Step 5: Ask the owner** to accept ADR-0053.
- [ ] **Step 6: Update "Where we are"** in `docs/README.md`, with deferred minors in this plan's closing notes.
- [ ] **Step 7: Commit** `docs: fixed texts in the person's language done`.
