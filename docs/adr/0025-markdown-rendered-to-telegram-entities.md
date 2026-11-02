# ADR-0025: Agents write Markdown; the Telegram transport renders it to message entities with goldmark

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-5), [ADR-0017](0017-extension-points.md), [ADR-0018](0018-own-telegram-bot-api-client.md)

## Context

The M2 transport sends plain text: "formatting is decided in M3". The live
M3a run showed what models do: they answer in Markdown. Telegram then
shows `**bold**` and list markers as raw characters.

Constraints:
- **ADR-0017:** Telegram types stay inside the transport, and `Outgoing`
  carries only channel-neutral fields.
- **R-DLV-5:** a long reply is split into parts, deterministically, so a
  resumed sender sends the same parts.
- **Length.** Telegram's 4096-character limit counts the text after its
  formatting is parsed, in UTF-16 units.

Telegram accepts formatting three ways:
- `parse_mode: HTML` (escape `&`, `<`, `>`);
- `parse_mode: MarkdownV2` (escape 18 characters, and models do not write
  it);
- plain text plus an `entities` array (type, offset and length in UTF-16
  units, and a language or URL where needed).

## Options

### A. Tell the model to write plain text

No formatting at all. Lists and code lose their structure.

### B. Markdown to Telegram HTML

The transport converts Markdown to HTML and sends `parse_mode: HTML`. A
rendering bug makes Telegram reject the message ("can't parse entities"),
so a plain-text fallback is needed. Splitting has to keep tags balanced.

### C. Markdown to plain text plus entities

The transport renders Markdown to visible text and an `entities` array.
There is nothing for Telegram to parse, so nothing to reject or escape.
The length limit applies to exactly the text we build, so offsets and the
limit use the same unit.

For C, the Markdown parser is a second choice:
- **C1. goldmark** (`github.com/yuin/goldmark`): CommonMark with the GFM
  extensions (tables, strikethrough), pure Go, no dependencies of its own,
  used by Hugo.
- **C2. Our own parser** for the subset models write, about 300 lines.

## Decision

**C with goldmark (C1).** The owner chose it.
- **Neutral text.** `Outgoing.Text` stays Markdown, the channel-neutral
  rich text; each transport renders it for its channel.
- **Telegram mapping:**
  - emphasis, strikethrough, inline code, code blocks with their language
    and links map to the matching entities;
  - headings become bold lines;
  - list items get `•` or numbers, indented by depth;
  - quotes become `blockquote`, and an `expandable_blockquote` above 500
    characters;
  - tables become a monospace `pre` block;
  - raw HTML is shown as text.
- **Splitting.** `Parts` splits the Markdown source between top-level
  blocks, packing whole blocks while the rendered part stays within the
  limit.
  - A code block that alone is too long is cut into several fenced
    blocks.
  - Any other block that alone is too long falls back to the M2 plain-text
    split.
  - The parts stay deterministic.

## Consequences

- **The persona.** The default persona says nothing about formatting;
  models write Markdown on their own.
- **A rendering bug costs a badly formatted message,** never a rejected
  one.
- **The dependency.** goldmark is a new dependency; the binary size and
  RSS are measured with `make memory`.
- **Other channels** (MQTT, a future web UI) write their own renderers
  from the same Markdown.
- **Same task, M3a task 11:**
  - a "typing…" indicator while a turn runs;
  - replying to the person's message (`reply_parameters`), with
    `GANGLION_TELEGRAM_REPLY` = `auto` | `always` | `off`. To make this
    possible, `event.Message` gains `Ref` (the channel's opaque message
    reference) and `Outgoing` gains `ReplyTo`. Both are channel-neutral.

## Revisit when

- goldmark stops being maintained.
- Telegram adds a formatting surface that entities cannot express.
