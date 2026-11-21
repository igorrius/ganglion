# ADR-0103: A command's answer records its command, and a reply brings its text only when it holds the node's words

- **Status:** Accepted
- **Date:** 2026-11-21
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-14, R-DLV-15), [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-13), [7.9](../spec.md#79-observability) (R-OBS-11); extends [ADR-0055](0055-a-reply-carries-the-replied-messages-origin.md) and [ADR-0073](0073-a-modules-message-names-its-module.md); [#297](https://github.com/igorrius/ganglion/issues/297)

## Context

On 2026-11-21 the owner replied "check it" to the node's `/status` answer,
whose verdict said one line needed a look. The agent saw the verdict and
the first lines of the Node section, not the line marked ⚠️, and said so.

A command's answer was stamped like any message of its turn: the turn's
kind, the current session, its taint. A reply to it took the "current
session" branch of ADR-0055, which brings a ~150-character excerpt because
the whole text is in the history already. A command writes no note of its
answer there (R-OBS-11 for `/status`; other commands write a short note of
what they showed, not the answer), so the rest of the text was nowhere.

The same branch let a module's words reach the model. `/modules` shows
text taken from a module's source and `/cells` shows a cell's `about`, which
R-RUN-13 shows the owner and never the model. A reply to such an answer
brought its first 150 characters as the agent's own words.

## Options

### A. A command's answer names its command; a short list of commands is quoted

The answer records the origin kind `command` and the command's word. A
reply to the answer of a command on a list in code, whose answer holds the
node's words alone, brings its text up to ~2,000 characters, without taint.
A reply to any other command's answer names the command and its time,
with no text, the channel's copy or a quote. The list starts with
`/status`.

### B. Decide "current" by whether the history holds the message

Keep the turn's origin, and bring the whole text when the session's history
does not hold it. Every command's answer would come back whole, `/modules`
and `/cells` included, as the agent's own words.

### C. Bring every command's answer as outside content

Keep the turn's origin, and wrap any command's answer as outside content
that taints the session. The agent reads `/status`, but the session is
tainted for asking about the node's own report, and host commands and
module writing then need a press or are refused.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| The agent reads `/status` | yes, whole | yes, whole | yes, whole |
| A module's words reach the model (R-RUN-13) | no, and today's leak closes | yes, the whole answer | as outside content |
| Asking about the node's report taints the session | no | no | yes |
| A new command needs a decision | yes: off the list until shown safe | no | no |

## Decision

A. It reads `/status` as the owner expects, keeps R-RUN-13 for every
command whose answer may hold a module's words, and closes the excerpt that
leaked them. The list is closed by default: a command joins it only when
its whole answer is the node's words.

## Security

- **Trust boundary:** a command's answer, which the node writes with no
  model, crossing into the model's history through a reply.
- **What an attacker controls:** a module's source (setting names, a
  cell's `about`, a journal note) shown by `/modules` or `/cells`; the
  channel's copy of a message and a selected quote; the origin row only by
  writing to the node's database.
- **Abuse cases and what stops them:**
  - A module's words reach the model through a reply to `/modules` or
    `/cells`: a command off the list brings no text, copy or quote (code:
    `quotedCommands`, `commandNote`).
  - A forged command word in the origin row, such as `/status` with a
    newline and an instruction: a word that does not match the node's
    command pattern makes the message one of an unknown source, wrapped as
    outside content (code: `commandOf`).
  - A selected quote from a quoted answer: it is the sender's own
    selection of the node's words, like a quote of the agent's message, and
    brings nothing new.
  - A value inside `/status` that is not the node's word (a skill's or a
    module's name): names are checked where they are made and shown
    escaped; a source that starts to show outside text takes `/status` off
    the list.

## Consequences

- A reply to `/status` lets the agent explain any line of it.
- A reply to `/modules`, `/cells` and other commands no longer brings an
  excerpt: the agent sees which command it was and the history's note of
  what it showed.
- Each new command or a change to a quoted command's answer has to keep its
  answer to the node's words, or leave the list.
- Answers stored before this change keep the turn's origin and read as
  before.

## Revisit when

A second command's answer needs to reach the agent, or `/status` starts to
show text a module or a page wrote.
