# ADR-0053: Send fixed texts in the person's language, from a catalogue with a one-time machine translation

- **Status:** Proposed
- **Date:** 2026-11-06
- **Deciders:** Igor Artasevych
- **Spec:** [7.13](../spec.md#713-the-language-of-fixed-texts), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-9), [7.9](../spec.md#79-observability) (R-OBS-8)
- **Design:** [p1-fixed-texts-language-design.md](../plans/p1-fixed-texts-language-design.md)

## Context

Everything the node writes without a model is in English: buttons, toasts, questions, the
lines a question folds to, command replies, limit and error messages. Only the command hints
follow the client's language (ruling R45), and ruling R46 keeps the rest in English.

People far from technology will use the bot and may not understand a message in another
language. Whoever installs a node may not be technical either, since a one-link install is on
the "Not now" list. So no step may rely on anyone reading English or knowing GitHub.

Constraints:

- **Tokens:** a fixed text costs no model call today, and the project's aim is the least model
  power with exact results.
- **Approvals:** some of these texts are what a person reads before pressing Approve. A model
  whose context holds outside content must not word them (spec principle 9).
- **Telegram** sends an optional `language_code` with each update, and advises falling back to
  the last recorded tag, then English.
- **Delivery:** a reply is rendered once and resent the same (R-DLV-5).

## Options

### A. A catalogue with shipped files only

Stable keys, English as default, `ru` and `uk` in the binary, more added by people. Costs no
tokens and nothing can go wrong at run time. A person whose language has no file reads English
buttons until someone contributes a file, which is the case the owner wants solved.

### B. A catalogue with a one-time machine translation in a clean call

As A, and a language with no file is translated once by the cheap model. The call has no tools
and no history, and takes only our English strings and a language name from a table in code.
Code checks the result, stores it in the database, and tells the person once that it may be
inaccurate. After that the texts cost nothing.

### C. The model words fixed texts on every message

No catalogue: fixed texts become part of the reply. It works in any language and costs tokens on
every message. It puts a model's wording, in a context that may hold outside content, on approval
questions.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| A person in any language understands | Only languages with a file | Yes, with a possible inaccuracy | Yes |
| Tokens | None | One call per language | Every message |
| Outside content can steer an approval's wording | No | No: clean context, only our strings | Yes |
| Texts are the same every time | Yes | Yes, once stored | No |
| What can go wrong at run time | Nothing | A bad translation, checked and marked | A wrong or injected text, unchecked |
| Effort | Lowest | Moderate | Low, but it undoes R-POL's fixed questions |

## Decision

**B.** It is the only option that serves a person in any language without spending tokens on every
message, and the danger that rules out C comes from a conversation's context, which the clean call
does not have.

- **Order:** the language a person chose, the last valid `language_code`, English. Per person, not
  per conversation.
- **Choosing:** the person says so in the chat, and the model calls `language.set` with a code. No
  command. In a clean session it applies at once. In a tainted one it asks with a button, in the
  requested language with the current one under it.
- **Priority:** an operator's file, a shipped file, a machine translation, per key, with English
  behind them.
- **Stays in English:** what a model reads, and `/last`, `/metrics`, `/modules` and admin alerts.
- **No plural rules:** texts are worded so a number never changes a noun's form.
- **The table of native names** bounds what can be asked for: `en`, `ru`, `uk`, `be`, `pl`, `cs`,
  `sk`, `sl`, `hr`, `sr`, `bg`, `de`, `nl`, `sv`, `nb`, `da`, `fi`, `et`, `lv`, `lt`, `fr`, `es`,
  `pt`, `it`, `ro`, `hu`, `el`, `tr`, `az`, `kk`, `uz`, `ka`, `hy`, `ar`, `he`, `fa`, `hi`, `bn`,
  `id`, `ms`, `vi`, `th`, `zh`, `ja`, `ko`.

This replaces ruling R46. R45 stays for shipped languages.

**Measured:** the tool's description adds a number of tokens to each turn, recorded here once task 6
of the plan has measured it.

## Security

Spec principle 9: every link is hostile, every input is untrusted.

- **Trust boundaries:**
  - the model's output becomes text on buttons and approvals, after code checks it;
  - `language_code` comes from the channel;
  - `language.set` is the first tool a person who is not an admin may call (the role is named `family`
    in the code today, which says nothing about who they are).
- **What an attacker controls:** a web page, through the model in a tainted session; a person's
  Telegram language setting. Our English strings, and the language name sent to the translation, are
  not theirs.

**Each abuse case and what stops it:**

| Abuse | What stops it, in code |
|---|---|
| A page has the model switch the language so that the person cannot read approvals | In a tainted session `language.set` asks, in the requested and the current language |
| A person changes someone else's language | The tool takes no target and writes the caller's own row |
| A model writes an approval's wording | The question comes from a file; the model supplies only a code, checked against the table |
| Outside text steers the translation | A clean call: no tools, no history, only our strings and a name from the table |
| A bad translation makes a dangerous approval read as safe | Symbols added by code on every button that acts; the command or address shown as is |
| A translation hides a link or an instruction | No URL, mention or markup beyond the source; the template is escaped before substitution |
| The agent rewrites a language file | Operator files sit outside the workspace, and a pack is in a table no tool writes |
| A forged `language_code` costs money | Only allowed people count; the table bounds it to one call per listed language, under a daily cap |

**What remains:** a translation can be wrong in a way code cannot see. So the person is told once,
and an approval's meaning does not rest on the words alone.

## Consequences

- A person in any listed language understands the bot, with no technical step by anyone.
- Every person-facing string moves behind a key, and a test keeps shipped files complete.
- A new text costs a translation per stored language, on next need.
- People who are not admins are no longer without tools, a change to a rule tests relied on.
- A person whose Telegram is in one language and who writes in another gets mismatched buttons until
  they say something. This follows the owner's rule.

## Revisit when

- A person reports a mistranslation that changed what they did: the marks and the checks were not
  enough.
- The tool's description cost shows up in the turn's budget.
- Rich Messages or another channel needs texts that are more than plain strings.
