# ADR-0054: Send structured replies as Telegram rich messages, from blocks the transport builds

- **Status:** Proposed
- **Date:** 2026-11-06
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-5, R-DLV-10, R-DLV-11, R-DLV-12), [ADR-0025](0025-markdown-rendered-to-telegram-entities.md), [design](../plans/p1-rich-messages-design.md)

## Context

[ADR-0025](0025-markdown-rendered-to-telegram-entities.md) renders the model's Markdown to message
entities. A table becomes a monospace block that wraps badly on a phone, a heading is a bold line, and a
reply above 4,096 characters becomes several messages. Its "Revisit when" named this case: a formatting
surface that entities cannot express.

Bot API 10.1 to 10.3 added rich messages: native tables, headings, lists, up to 32,768 characters, 500
blocks, 16 levels and 20 table columns in one message. The owner reads a daily report in this form from
OpenClaw and wants it here.

Constraints:

- **The aim of the project:** the least model power. The model keeps writing one ordinary Markdown, with
  no change to the prompt (owner).
- **Spec principle 9:** the model's text may repeat a hostile page.
- **R-DLV-5:** a message is never lost, and parts are deterministic.
- **The Bot API documents no field** saying whether a person's app can show a rich message. OpenClaw's
  help says some apps show them as unsupported, and leaves its setting off. The owner's apps all show
  them (owner, 2026-11-06).
- **A rich message has a separate field** when it arrives (`rich_message`), and the node reads only `text`
  and `caption` today. The bot's own answers would become messages it cannot read when forwarded back.

Two choices are separate here: how the message is described to Telegram, and which messages use the format.

## Options

### How the message is described

**A. The `markdown` field.** Pass the model's text on. Telegram parses it. No renderer to write. Rich
Markdown "can contain arbitrary HTML", and it parses `$...$` as a formula.

**B. The `html` field.** Convert Markdown to HTML and pass that. The same parser sits behind it; a rendering
bug is a refused message; the model's raw HTML has to be escaped correctly every time.

**C. The `blocks` field.** Build typed blocks from goldmark's tree, from an allowlist. Nothing parses the
text. More code than A, and a mapping to keep.

### Which messages use it

**D. Every message.** One path, and long replies become one message. Every message then depends on the
person's app, and questions, plans and folds change.

**E. Only messages with a table or a heading.** Models put a heading on small answers too, so the format
would still be used where it adds little.

**F. Only messages with a table or at least two headings.** A table is what an ordinary message cannot show,
and two headings mean a report with sections. One heading stays a bold line.

## Comparison

| Criterion | A. `markdown` | B. `html` | C. `blocks` |
|---|---|---|---|
| Model's text reaches a parser | yes | yes | no |
| An injected button or media URL | possible | only if escaping fails | cannot be built |
| Effort | least | middle | most |
| A rendering bug costs | a wrong look | a refused message | a refused message, then the fallback |

| Criterion | D. every message | E. table or heading | F. table or two headings |
|---|---|---|---|
| Used only where needed (owner) | no | often not | yes |
| Risk on an app that cannot show it | all replies | many | fewest |
| Questions and plans untouched | needs exclusions | yes | yes |
| A report with sections and no table | rich | rich | rich |
| One short answer with one heading | rich | rich | ordinary |

## Decision

**C and F (owner on F).** Blocks because the attacks of A and B stop being possible rather than being
filtered. A table or two headings because the owner wants the format used only when it is necessary.

- **Setting:** `GANGLION_TELEGRAM_RICH=on|off`, default `on` (owner).
- **Excluded:** a message with buttons, an edit, an attachment, a fold or a superseded answer.
- **A refusal is not a loss:** a 400 from `sendRichMessage` sends the same text the ordinary way.
- **A rich message that arrives is read,** as outside content (owner on reading).
- **A symbol tree** is shown as a preformatted block, with the setting on or off.

The rest is in the [design](../plans/p1-rich-messages-design.md). ADR-0025 still holds for every other
message; its mapping of tables and headings no longer holds for a message that goes rich.

## Security

- **Trust boundaries:** model's output to Telegram, and another bot's message to the model.
- **What an attacker controls:** the model's text through a web page, and all of a rich message forwarded
  to the bot, collapsed blocks and spoilers included.

| Abuse | What stops it |
|---|---|
| A drawn button beside a real approval | No block of type `buttons` is built, and no parser sees `<tg-button>` |
| A picture URL that leaks data with no press | No media block is built; an image becomes a link, and the app asks before opening it |
| A `tg://` link | Only `http` and `https` become links |
| A structure built to break delivery | Limits in `Parts`; a refusal falls back |
| Instructions hidden in a forwarded rich message | All of its text is wrapped as outside content and taints the session, also with no `forward_origin` |
| A forwarded message posing as a command | `Forwarded` keeps it from running as one, as for any forward |
| A button's data in a forwarded message | Buttons are not read |

**Not closed here:** URL previews. A URL the model writes may be fetched by Telegram, on ordinary messages
today and on rich ones, where `sendRichMessage` has no parameter for it. Spec, "Not now".

## Consequences

- **Easier:** a table reads as a table, a report fits one message, and no token is spent.
- **Harder:** a second renderer to keep in step with `markdown.go`, and a third of the logic in `Parts`.
- **Given up:**
  - links and formatting inside a symbol tree;
  - an app that accepts a message and cannot show it is not detected, so `off` is the only answer;
  - forwarding the bot's own answer taints the session, as any forward does.
- **To build:** the blocks, the two tests in `Parts` and `Send`, the fallback, reading, the fake Bot API's
  `sendRichMessage`, a metric.

## Revisit when

- A person sees "unsupported" on a current app, or the repository opens to testers: reconsider the default.
- The live look shows a wide table still does not fit a phone.
- Long replies with no structure turn out to read badly as several messages.
- The Bot API adds a field for what an app can show.
