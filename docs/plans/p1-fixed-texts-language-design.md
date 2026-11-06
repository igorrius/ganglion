# Fixed texts in the person's language: design

Status: approved by the owner on 2026-11-06. It feeds the spec changes, the ADR and the
step-by-step plan. It is not itself a plan.

Marks, as in the [M3d design](p1-m3d-host-commands-design.md): **(owner)** the owner
decided it; **(call)** the author's call, open to correction; **(builder)** left to
whoever implements it.

## 1. Why

Everything the node writes without a model is in English today: buttons, toasts,
questions, the lines an answered question folds to, command replies, limit and error
messages. Only the command hints follow the client's language (ruling R45), and ruling
R46 keeps the rest in English.

The owner is a developer, but the people who will use the bot may be far from all of
this, and may not understand a message in another language **(owner)**. Whoever
installs a node may not be technical either: a one-link install is in the spec's "Not
now" list, so the admin cannot be assumed to read English or know GitHub **(owner)**.

**What success looks like:** a person who knows only Polish installs or is given a bot
and understands what it writes to them, buttons included, without doing anything
technical, and knows when a text was translated by a machine.

## 2. Goals, non-goals, anti-goals

**Goals:**

1. Fixed texts a person reads go out in that person's language.
2. The language is Telegram's unless the person chose another **(owner)**.
3. A person chooses by saying so in the chat; there is no command and no menu
   **(owner)**.
4. A language with no file gets one translated once by a model, marked for the reader
   as a machine translation **(owner)**.

**Non-goals:**

- Translating what only the model reads: prompts, tool results and tool errors.
- Translating admin diagnostics for now: `/last`, `/metrics`, `/modules`, admin alerts
  **(call)**.
- Telegram Rich Messages, which come after this, separately **(owner)**.
- Asking a person or an admin to open a GitHub issue **(owner)**.

**Anti-goals, which make it a failure even if it works:**

- A model's wording reaches an approval from a conversation that read outside content.
- A question's translation hides what the button does.
- A token spent on fixed texts on every message.
- A second message or a repeated notice that says nothing new
  ([R-POL-9](../spec.md#76-action-policies-and-risk-classes), the chat stays clean).

## 3. Which language a person gets

For each person (a principal, not a conversation) **(call)**, in this order:

1. the language the person chose;
2. the last `language_code` Telegram sent for them;
3. English.

Step 3 follows Telegram's own advice for a missing `language_code`: the last recorded
tag, then English.

- **Normalising:** only the primary subtag counts (`pt-br` becomes `pt`), validated
  against the table of native names in section 5.5 **(call)**. A code not in the table
  counts as missing.
- **Where the language applies:**
  - a reply or a question: the person the turn is for;
  - a toast or a folded line: the person who pressed;
  - a scheduled message: the job's owner;
  - a group with no person behind a message: English **(call)**.
- **Delivery stays deterministic (R-DLV-5):** a fixed text is rendered when its outbox
  row is written, so a resumed sender sends the same text after a language change.
- **Known mismatch, accepted:** a person with an English Telegram who writes Russian
  gets Russian replies and English buttons until they say something. That follows the
  owner's rule, and the person has a way out.

**What the transport adds:** the Telegram `user` type gains `language_code`, read from
messages and button presses, and the channel-neutral event carries it as a language.
A person's row is written only when the value changed, so a message costs no extra
write **(builder)**.

## 4. Choosing a language in the chat

The person says "I don't understand, write in Ukrainian". The model reads that message
anyway, so choosing costs no extra turn. It calls a new tool, `language.set(code)`.

| Situation | What happens |
|---|---|
| The session has read nothing from outside | Applied at once; the reply is in the new language **(owner)** |
| The session is tainted | A question with buttons, in the requested language with the same question in the current one under it, and applied on the press **(owner)** |
| That language is already chosen | Nothing changes |
| The code is not in the table | The tool returns an error to the model; nothing changes |

- **Telling a request apart:** "translate this into Polish" is not a choice of language.
  The tool's description says so. A wrong switch is cheap: the person sees it and says
  so. A chatlab scenario covers both phrasings with a real model.
- **Who has it:** every allowed person, including a `family` member, who otherwise gets
  no tools in M3b. It writes only the caller's own row, so the exception gives them
  nothing over anyone else **(call)**. Scheduled runs are not offered it, since no
  person is there to ask.
- **The question's text comes from code**, from the files of both languages; the model
  supplies only the code. Two lines, because a request from an injected page looks the
  same to code as a real one: a person who cannot read the current language reads the
  first line, and a person shown a language they never asked for reads the second
  **(owner)**. If the two languages are the same text, one line goes out. A language with
  no file yet has an English first line with its native name:
  `Switch to Polski? The bot's own buttons stay in English for now.`
- **The first-run introduction** already asks which language to speak. Its rules
  (`IntroductionRules` in `agent/prompt.go`) add calling this tool, so an installer
  whose language has no file gets the translation from the start.
- **What the model is told:** a person's *chosen* language adds one short line to the
  per-turn notes in the last user message, as with the current time, so the system
  prompt and its cache do not change. With no choice nothing is added, and the model
  keeps following the conversation as today, so an explicit choice is the only thing
  that costs tokens **(call)**.
- **Tool description cost:** it is sent on every turn. Keep it short, measure the tokens
  it adds to a turn, and report the number in the ADR **(builder)**.

## 5. Language files

### 5.1 The catalogue

A new package holds every person-facing text under a stable key. English is the default
and is compiled in; `en`, `ru` and `uk` ship in the binary as embedded files **(owner)**.
Lookup falls back key by key: the person's language, then English. Texts are plain
strings with named placeholders such as `{name}`; see "What plain means" below.

**Which texts move (call):** every string a person sees, an admin included, since the
person installing a node may read only their own language. That covers the replies, toasts,
labels and lines in `agent/ack.go`, `agent/commands.go`, `agent/prompt.go`,
`agent/resume.go` and `tool/pending/pending.go`; the approval questions in `tool/builtin`
(the `Question` methods and their templates); and the task list and its buttons in
`schedule_quick.go`. Only the diagnostics listed under non-goals stay in English, and
strings a model reads stay where they are. The inventory goes into the plan, since a rough
count (about 200 candidates) mixes both kinds.

**What "plain" means (call):** only our template is neutralised, by escaping it before
substitution; `Transport.Send` and its Markdown rendering stay as they are. Values put into
a template (an address in backticks, the text a `message.send` would send) keep today's
handling. So a translation cannot add formatting, and no message changes shape.

### 5.2 Plural forms

**No plural rules (call).** Texts are worded so that a number never changes a noun's
form ("Tasks: 3", not "3 tasks"). Plural rules differ per language, a machine
translation could not be checked against them, and a table for each language would be
one more thing to get wrong. This closes the spec's open question on plural forms.

### 5.3 A test

A test fails the build when a shipped file misses a key, has a key English does not, or
changes a text's placeholders. It also fails on a text over its limit (Telegram's
200-character toast, and a short limit for a button label).

### 5.4 Priority and storage

Highest first: an operator's file, a shipped file, a machine translation.

- **An operator's file** lives in a `languages/` directory beside the database, outside
  the workspace, for the reason given for packs below: planned writers such as host
  commands and agent-written skills must not reach what buttons say. It is read at start
  and goes through the same validation as a machine translation **(call)**.
- **A machine translation** lives in a database table, not in the workspace, because the
  agent's own tools can write to the workspace and must not reach what buttons say. No
  tool writes to this table **(call)**.

### 5.5 The table of native names

A table in code maps a code to the language's name in that language (`pl` → `Polski`).
It bounds which languages can be asked for, and so what a person can cost the node
**(call)**. It holds the languages a model translates well; the builder picks a list of
a few dozen and notes it in the ADR.

## 6. Machine translation

**When (lazily, owner, 2026-11-06):** when the node is writing to an allowed person in a language
that is in the table and has no file. A language nobody is being written to costs nothing, so a removed
person needs no special case, and a text changed by a release is translated when next needed.
Strangers never reach it, since they get no reply. It needs no one's permission
**(owner)**. A language requested in a tainted session starts its translation only after
the press, so an injected request spends nothing **(call)**.

**How it stays out of the way:**

- It runs in the background and never holds a turn. Until it finishes, the fixed texts
  are in English.
- It is one call per language on the role used for checks (Haiku today), after which
  the texts cost nothing.
- It survives a restart; a failure retries later under a daily cap, then leaves English
  in place **(builder)**.

**Why it is safe (the owner's question on this was whether a model may touch these
texts at all):** the danger was a translation inside a conversation, where outside text
could steer the model. Here the call has a clean context, no tools and no history. Its
only inputs are our English strings and a language name from the table, neither of which
an outsider controls.

**What code checks before accepting the result:**

- it is JSON with exactly the expected keys;
- each text has the same placeholders as the English one;
- each text is within its limit;
- no text holds a URL, a mention or markup its English source lacks;
- a text that fails is left in English; the rest are kept, and a pack missing the
  approval texts is refused whole.

**A changed English text:** the pack stores a hash of the English source per key. A
changed or new key is translated on the next need, and is in English until then. So a
new release costs only the texts that changed.

**A mistranslation on a button:** every button that acts carries a symbol added by code,
outside the translated text, so it reads correctly whatever the words say: ✅ and ❌ on
approvals, 🗑 on `Delete` in the task list, which acts on the press with no question
**(call)**. In a question, the command, address or file is always shown as is, outside the
translated template.

**The mark the person sees (owner):** the first time a person is written to in a language
whose texts come from a machine translation, one line in that language says that the
buttons and system messages were translated automatically and may be inaccurate.

- Once per person per language, recorded in the person's row.
- It goes with the next message the node sends them after the pack is ready.
- It is not shown when an operator's or a shipped file covers the language.
- It is itself in the pack. If that key failed the checks, the line goes out in English.

**Later, a person's correction** ("this button is translated wrong") is not part of this.
An operator's file overrides a pack, and a developer can contribute a reviewed file to
the project **(call)**.

## 7. Where the data lives

A new table, by migration (exact shape is left to the builder):

- per person: chosen language, last seen Telegram language, the languages whose machine
  notice was shown;
- per language and key: the translated text, the hash of its English source, when it was
  made and by which model.

**Command hints (R45)** stay as they are for shipped languages; a machine pack sets its
hints with `setMyCommands` once it is ready **(call)**.

## 8. Security

Spec principle 9: every link is hostile.

| Abuse | What stops it, in code |
|---|---|
| A web page tells the model to switch the language so that the person cannot read approvals | In a tainted session `language.set` asks with a button, in the requested and the current language; the person can refuse |
| A person changes someone else's language | The tool takes no target; it writes the caller's own row |
| A model writes an approval's wording | The question comes from a file; the model supplies only a code, checked against the table |
| Outside text steers the translation | Clean context, no tools, only our strings and a name from the table |
| A bad translation makes a dangerous approval read as safe | Symbols added by code on every button that acts, the command shown as is, the validation above |
| A translation hides a link or an instruction in a button | No URL, mention or markup beyond the source; the template is escaped before substitution |
| The agent rewrites a language file | Operator files sit outside the workspace, and the pack is in the database |
| A forged `language_code` costs money | Only allowed people count, and the table bounds it to one call per listed language |
| The agent edits the pack through its file tools | The pack is in the database and no tool writes it |

**What remains:** a machine translation can still be wrong in a way code cannot see, such
as a toast or a question's sentence that reads differently. That is why the person is
told, and why the approval's meaning does not rest on the words alone. A person who cannot
read the text can still refuse a question simply by not pressing, and it expires.

## 9. Observability (R-OBS-8)

In the same commits as the code:

- fixed texts sent, by language and source (operator, shipped, machine, English fallback);
- languages asked for, by how they were chosen (Telegram, the person);
- translation runs by outcome, with tokens and cost, and texts refused by the checks;
- `language.set` calls by outcome.

The metrics show which languages people want, which replaces asking anyone to file an
issue.

## 10. How we know it works

- **`make test`:** the file test of 5.3; the order of section 3; a scripted model for
  `language.set` in clean and tainted sessions; the translation's checks against seeded
  bad output (a dropped placeholder, an added URL, a missing key); restart during a
  translation; the notice sent once.
- **Chatlab:** one new scenario for "write in Polish" against "translate this into Polish",
  with a real model; only changed scenarios run. The full lab is never started without the
  owner's decision.
- **Memory and size:** report the binary and RSS after the catalogue, as earlier milestones
  did.
- **The owner, on the test bot:** Ukrainian and English from Telegram's setting, a spoken
  switch, a language with no file, and reading the notice.

## 11. What changes in the documents

- **Spec:** new requirements for the language of fixed texts and for the tool; ruling R46
  replaced; the "Not now" entry removed.
- **ADR:** one new ADR, Proposed, covering the catalogue, the order of languages, the tool
  and the machine translation, with its options compared. It supersedes nothing accepted,
  since R46 is a ruling.
- **Architecture:** the new package in `docs/architecture/05-code-map.md`; `docs/README.md`
  when it closes.

## 12. Left to the builder

- The package's name and the file format's details, YAML being already in use.
- The exact list in the table of native names.
- How the background translation is scheduled, as long as it survives a restart and never
  holds a turn.
- The inventory of texts, and which go in the first commit.
