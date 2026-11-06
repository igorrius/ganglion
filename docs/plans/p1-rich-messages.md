# P1 · Telegram Rich Messages — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** a message with a table or at least two headings goes out as a Telegram rich message built from
blocks the transport controls; a symbol tree keeps its levels; a rich message sent to the bot is read.

**Architecture:** all of it is in `transport/telegram`, with no field added to `event.Outgoing` and no
change to the agent or the prompt. `rich.go` turns goldmark's tree into blocks, `tree.go` keeps a tree's
lines, `richread.go` flattens an incoming rich message. `Parts` and `Send` each apply one test.

**Tech stack:** Go 1.27, goldmark (already a dependency). No new dependency.

**Spec:** [p1-rich-messages-design.md](p1-rich-messages-design.md), approved by the owner on 2026-11-06,
and [ADR-0054](../adr/0054-structured-replies-as-rich-blocks-the-transport-builds.md). Read them with this
plan.

## Global Constraints

- All documentation, comments and doc comments are in English; [conventions](../conventions.md) apply.
- Architecture docs change in the same commit as the code they describe.
- A metric and a log line come in the same commit as what can fail (R-OBS-8). Labels from a closed set;
  never a conversation or message text.
- ADR-0054 stays Proposed until the owner accepts it.
- No new Go dependency. No `time.Sleep` in tests.
- Never a `markdown` or `html` field in `rich_message`; never a block outside design section 4.
- A full chatlab run is never automatic: `make lab` for what changed; ask the owner before `make lab-all`.
- Commit only with the owner's go-ahead; never on `master`. Work on branch `rich-messages`.

## Review Focus

Failure modes the design implies and no other test would catch. Each has its test in the named task.

1. **Structure inside a code fence** (`# not a heading`, `|---|`): it does not make a message rich, and a
   tree in a fence stays as it is (tasks 2, 3).
2. **An awkward table:** a short row, an empty cell, a `\|` in a cell, a link in a cell, 21 columns
   (task 3).
3. **A 400 that is not about the format,** such as a thread that is gone: the ordinary send fails too, the
   message is failed once as today, and nothing is sent twice (task 4).
4. **A resumed multi-part rich message:** the same parts under the same keys after a restart (task 5).
5. **A rich message with hostile content sent to the bot:** text hidden in `details`, a spoiler or a caption
   is wrapped as outside content; a forged command does not run; a button's data never reaches the agent
   (task 6).

---

### Task 1: The decision on paper

**Files:** `docs/adr/0054-structured-replies-as-rich-blocks-the-transport-builds.md`, `docs/adr/README.md`,
`docs/spec.md`.

- [x] **Step 1:** ADR-0054, Proposed, and its row.
- [x] **Step 2:** R-DLV-11 and R-DLV-12 in the spec; "Not now" updated.
- [x] **Step 3: Commit** with the design and this plan, on branch `rich-messages`.

### Task 2: A symbol tree keeps its levels

Changed on the owner's look, see [design section 5](p1-rich-messages-design.md#5-a-symbol-tree): the tree stays
text with no-break spaces rather than the preformatted block below, and `treeLeads` replaced `treeSource`.

**Files:**
- Create: `transport/telegram/tree.go`, `transport/telegram/tree_test.go`
- Modify: `transport/telegram/markdown.go` (case `*ast.Paragraph, *ast.TextBlock` of `block`)

**Interfaces:**
- Produces: `func treeSource(src []byte, n ast.Node) (string, bool)`: the block's lines as written, with
  leading spaces, inside a list item without the item's own indentation; `false` when no line is a tree line.
- Produces: `func isTreeLine(line string) bool`: true when, after any spaces and `│`, the line starts with
  `├` or `└`.

- [x] **Step 1: Write the failing tests**
  - `TestRenderKeepsTreeLevels`: a tree with a root line and lines indented with spaces and with `│`. Rendered
    text equals the source lines, one `pre` entity over all of it.
  - `TestRenderTreeInAList`: the same inside `- item`, with the item's indentation removed.
  - `TestRenderNotATree`: not preformatted: prose with `└` mid-line; `plan.Render` output for a step whose text
    contains `└`, copied as a literal with a mark from `plan/plan.go`, since the transport does not import
    `plan`; `│` alone.
  - `TestRenderTreeInFenceUnchanged`: a fenced tree gives the same result as before this change.
- [x] **Step 2: Run to see them fail.** `go test ./transport/telegram -run 'Tree' -count=1`.
- [x] **Step 3: Implement.** goldmark trims each line's start, so walk back from a segment's `Start` to the
  previous newline in `src`.
- [x] **Step 4: Run** `go test ./transport/telegram -count=1`: PASS.
- [x] **Step 5: Commit** with `docs/architecture/05-code-map.md`.

### Task 3: Markdown to blocks

**Files:**
- Create: `transport/telegram/rich.go`, `transport/telegram/rich_test.go`

**Interfaces:**
- Consumes: `treeSource` (task 2); `md`, the goldmark instance of `markdown.go`.
- Produces:
  - `type richMessage struct { Blocks []richBlock `json:"blocks"` }`; `richBlock` and rich text types
    marshal to the Input forms of design section 4, and nothing else.
  - `func renderRich(src string) richMessage`.
  - `func structured(src string) bool`: a table, or at least two headings, at any depth, by the parsed tree.
  - `type richSize struct{ Chars, Blocks, Depth int }`; `func (m richMessage) size() richSize`; `func (s
    richSize) fits() bool`.
  - Constants `maxRichChars = 32768`, `maxRichBlocks = 500`, `maxRichDepth = 16`, `maxRichColumns = 20`. `Chars`
    counts runes; list items and table rows count as blocks; a refusal is covered by task 4 if Telegram
    counts differently.

- [x] **Step 1: Write the failing tests**
  - `TestRenderRichBlocks`, table-driven, one row for each row of design section 4, comparing marshalled JSON.
    Include ordered list `value` from a start of 3, heading sizes 1 and 6, a long quote of paragraphs as
    `expandable_blockquote`, a long quote holding a list as `blockquote`.
  - `TestRenderRichTables`: header cells `is_header`; alignment from `:---:`; `valign: "top"`; a short row
    padded; a `\|` kept in the cell; a link in a cell; 21 columns become `pre`.
  - `TestRenderRichHostile`: for `<tg-button type="callback_data" data="x">ok</tg-button>`,
    `![](http://evil.example/?d=1)`, `[a](tg://user?id=1)`, `[a](javascript:alert(1))`, `$x^2$`,
    `<details>hide</details>`: the JSON holds no key `markdown` or `html`, every `type` is in the allowlist of
    `TestRichAllowlist`, no `tg://` or `javascript:` in a `url`, and the image is a `url` text, not a media block.
  - `TestStructured`: true for a table, two `#` headings, two headings inside a quote; false for one heading, a
    plain list, headings and a table only inside a code fence.
  - `TestRichSize`: blocks counted at each limit.
- [x] **Step 2: Run to see them fail.** `go test ./transport/telegram -run 'Rich|Structured' -count=1`.
- [x] **Step 3: Implement** in the shape of `writer` in `markdown.go`, with one function per block kind
  (`gocognit` caps at 20). A tree paragraph is a `pre` block.
- [x] **Step 4: Run** `go test ./transport/telegram -count=1`: PASS.
- [x] **Step 5: Commit.**

### Task 4: Sending, the setting, the fallback

**Files:**
- Create: `transport/telegram/richsend.go`, `transport/telegram/richsend_test.go`
- Modify: `transport/telegram/transport.go`, `client.go`, `types.go`, `telegramtest/server.go`,
  `telegramtest/server_test.go`, `cmd/flags/flags.go`, `cmd/run.go`, `cmd/cmd_test.go`, `config/bootstrap.go`,
  `config/delivery.go`, `docs/architecture/05-code-map.md`

**Interfaces:**
- Consumes: `renderRich`, `structured` (task 3).
- Produces:
  - `func WithRichMessages(on bool) Option`; the default in `New` is off, since zero means off; the flag's
    default is `on`.
  - `func (t *Transport) richShape(msg event.Outgoing) bool`: setting on, and no `Edit`, `EditOf`, `Ack`, `Attach`,
    `Buttons`, `Folded` or `Superseded`.
  - `func (c *client) sendRichMessage(ctx context.Context, p richParams) (message, error)`.
  - In `telegramtest`: `Outgoing.Rich bool`. `sendRichMessage` flattens blocks to `Text` (table cells joined by
    ` | `), appears in `Sent` and `Showing`, answers 400 on a `markdown` or `html` field, an unknown block type or
    a limit passed, and works with `FailNext`.
  - Setting: flag `telegram-rich`, env `GANGLION_TELEGRAM_RICH`, values `on`, `off`, default `on`;
    `config.Bootstrap.TelegramRich bool`.
  - Counter `ganglion.telegram.rich` with label `outcome`, a named type: `sent`, `refused` (task 6 adds `read`,
    `unreadable`).

- [x] **Step 1: Write the failing tests**, through `New` and the fake:
  - `TestSendRichWhenStructured`: a table goes through `sendRichMessage`, with reply, thread and silent carried;
    one heading goes through `sendMessage`.
  - `TestRichExclusions`: table-driven, one row per excluded field: goes the ordinary way.
  - `TestRichOff`: `WithRichMessages(false)`, a table goes the ordinary way as today.
  - `TestPlanStaysOrdinary`: a plan's first text as a literal, in the form of `plan.Render`: ordinary.
  - `TestRichRefusedFallsBack`: `FailNext("sendRichMessage", 400, 0)`: one `sendMessage`, the reference returned,
    the counter's `refused` is 1.
  - `TestRichRefusedLongSplits`: a refused 10,000-character message with a table: ordered `sendMessage` calls, all
    text delivered, last reference returned.
  - `TestRichRetryableNoFallback`: 429 gives `RetryAfterError`; a 500 is returned; no `sendMessage` call.
  - `TestRichThreadGoneFailsOnce`: a topic that is gone: `sendRichMessage` 400, then `sendMessage` 400, one
    `ErrPermanent`, nothing delivered.
  - Flag: extend `cmd/cmd_test.go`'s env tables, with an invalid value refused.
  - Server: `telegramtest/server_test.go` for each 400 above.
- [x] **Step 2: Run to see them fail.**
- [x] **Step 3: Implement.** In `Send`, after the attachment and ack branches:
  `if t.richShape(msg) && (structured(msg.Text) || renderedUnits(msg.Text) > maxTextUnits)` call a new
  `sendRich`, which on a `400` logs at warn (message id and kinds only), counts `refused` and calls the ordinary
  path, chunked by `splitMarkdown` when it does not fit. `sendRichMessage` joins `sendError`'s mapping unchanged.
- [x] **Step 4: Run** `go test ./transport/telegram/... ./cmd/... ./config/... -count=1`: PASS; `make lint`.
- [x] **Step 5: Commit** with the code map.

### Task 5: Length

**Files:**
- Modify: `transport/telegram/split.go`, `transport/telegram/split_test.go`

**Interfaces:**
- Consumes: `richShape`, `structured`, `renderRich`, `richSize.fits`.
- Produces: `func splitRich(src string) []string`; `func splitTable(block string) []string`, which cuts a table's
  source between rows and repeats its header and delimiter lines.

- [x] **Step 1: Write the failing tests**
  - `TestPartsRichOneMessage`: a table with 20,000 characters: one part, key unchanged. With the setting off: parts
    as today.
  - `TestPartsPlainLongUnchanged`: 10,000 characters of paragraphs and one heading: the same parts as today.
  - `TestPartsRichOverLimit`: 40,000 characters with two headings: each part fits, all text present, keys `:p0`…,
    `ReplyTo` on the first only.
  - `TestPartsRichBigTable`: 600 rows: each part has the header and fits; every row once.
  - `TestPartsRichDeterministic`: two calls, the same parts; `Send` on each part delivers a rich message when
    above 4,096 units even with no heading of its own.
  - `TestPartsMixedCases`: a long question with a table: buttons on the last part only, and it goes ordinary; text
    overflowing a caption with a table goes rich.
- [x] **Step 2: Run to see them fail.**
- [x] **Step 3: Implement.** In `Parts`, after the `Attach` branch and before the ordinary test: `if
  t.richShape(msg) && structured(msg.Text)`. Reuse `mdBlocks` and `textParts`; a non-table block too long falls to
  `nextCut`.
- [x] **Step 4: Run** `go test ./transport/telegram/... ./outbox/... -count=1`: PASS.
- [x] **Step 5: Commit.**

### Task 6: Reading a rich message

**Files:**
- Create: `transport/telegram/richread.go`, `transport/telegram/richread_test.go`
- Modify: `transport/telegram/types.go` (`RichMessage json.RawMessage` with `json:"rich_message,omitempty"` on
  `message`), `transport/telegram/transport.go` (`messageEvent`), `agent` tests only if needed

**Interfaces:**
- Produces: `func readRich(raw json.RawMessage) string`: the flattened text by the table of design section 11; "" if
  nothing is readable or the JSON is invalid. Depth stops at `maxRichDepth`.

- [x] **Step 1: Write the failing tests**
  - `TestReadRich`, table-driven, one row per row of design section 11, including rich text as a string, a list and
    a nested object, and a file block giving its caption and the word that a file was there, with no `file_id`.
  - `TestReadRichDepth`: 40 levels of nesting: returns, no panic.
  - `TestMessageEventRich`: an update with `rich_message` and no `text`: `Message.Text` is the text and `Forwarded`
    is true, with and without `forward_origin`. With `text` present, `text` is used.
  - `TestMessageEventRichUnreadable`: only a divider: an event with the note `[a rich message with nothing
    readable]`, counter `unreadable`; `read` for the others.
  - `TestRichContentIsOutside`, in `agent`: a command-like `/new` in such a message runs no command and is wrapped;
    a `callback_data` in a buttons block does not appear in the content.
- [x] **Step 2: Run to see them fail.**
- [x] **Step 3: Implement.** Unknown block types add nothing. Confirm the size against the agent's limits on a user
  message and, if there is none, cap with `internal/text` at `maxRichChars`.
- [x] **Step 4: Run** `go test ./transport/telegram/... ./agent/... -count=1`: PASS.
- [x] **Step 5: Commit.**

### Task 7: Closing

**Files:** `docs/README.md`, `docs/architecture/05-code-map.md`, `tools/chatlab/passed.json`.

- [x] **Step 1:** `make test`, `make lint`, `make memory`: 46.1 MB binary, 44.2 MB RSS after 1,000 turns. `make lab-faults` not run: no seeded fault touches this work, and it spends subscription limits.
- [x] **Step 2:** `make lab` for what changed. If it recommends a full run, ask the owner.
- [x] **Step 3:** A fresh reviewer reads the whole branch against the design, the ADR's Security section first.
- [ ] **Step 4: The owner's live look on @ganglion_test_bot**, from design section 8: a table on a phone, a wide one
  with and without `is_compact`, headings and lists, their own tree, a reply to a rich message, a report above 4,096
  characters, the bot's own rich answer forwarded back. An empty table cell's form is settled here too.
- [ ] **Step 5:** Fixes from the look. "Where we are" in `docs/README.md`. ADR-0054 Accepted when the owner says so.
- [ ] **Step 6: Commit,** and the owner's merge.
