# Telegram widgets (Bot API 10.4), read for the node

- **Date:** 2026-11-16
- **For:** the spec's "Not now" list (section 14); a future ADR on who
  writes a widget's HTML
- **Question:** Bot API 10.4 (2026-11-09) added HTML widgets in a bot's
  messages. What are they, what do they cost the node, and where would the
  node use them?
- **Status:** read from the
  [changelog](https://core.telegram.org/bots/api-changelog),
  [the Bot API](https://core.telegram.org/bots/api) and
  [the widgets page](https://core.telegram.org/bots/widgets) on
  2026-11-16. Nothing was sent or run.

## What a widget is

A widget is one self-contained HTML document with inline CSS and
JavaScript, drawn inside the bubble of a bot's message. It is not a Mini
App: there is no page to serve and no `initData` to check.

| What | Found |
|---|---|
| Methods | `sendWidget`, `sendSharedWidget`, `answerWidgetQuery`, `editMessageWidgetData`, `editSharedWidgetData` |
| Types | `Widget` (`html`, `width`, `height`, `id`, `public_data`, `chat_data`); `widget` on `Message` and `ExternalReplyInfo`; `widget_query` and `shared_widget_query` on `Update`; `WidgetQuery`, `SharedWidgetQuery`, `WidgetQueryType*`, `WidgetQueryWallet` |
| Code size | At most 32,768 bytes of UTF-8 and at most 4,096 bytes after zlib level 9; else `INPUT_MEDIA_WIDGET_TOO_LARGE`. No NUL |
| Code after sending | Fixed. Only the caption can be edited |
| Runtime | QuickJS, not a WebView. `fetch`, `XMLHttpRequest`, `WebSocket` throw; no outside scripts, styles, fonts or images (images as `data:` or inline SVG); no navigation |
| Runtime limits | 300 ms of JavaScript per second, 500 ms per task, 8 MB, 2,000 nodes, 64 timers; two crashes in a row stop it until the app restarts |
| Size on screen | Bubble 200–600 px wide, 50–600 px high; clipped, nothing scrolls |
| Theme | `--tg-theme-*` CSS variables and `themeParams`, as in Mini Apps |
| State | `chat_data` (one message) and `public_data` (shared by all copies of a shared widget), text of up to 1,024 bytes each, not read by the server |
| Who writes state | Only the bot: `editMessageWidgetData`, `editSharedWidgetData` (one edit a second for a shared widget). An edit is silent |
| Widget to bot | `TelegramWidget.request(data)`, 1–64 bytes, mostly on a real tap. The bot gets `widget_query` and answers with `answerWidgetQuery` (up to 1,024 bytes) within 15 s, else the widget sees `TIMEOUT`. 5 requests per 5 s per user and bot. An answer changes no state |
| Identity | `from` is the user Telegram vouches for; no signature to check. `data` is untrusted, like `callback_data` |
| Other calls | `openLink` (`https:`, up to 256 characters, the user confirms), `openTelegramLink`, `copyText`, `HapticFeedback`; `localStorage` of 4 KB per message on the device |
| Where, in production | Private chats with the bot only. Groups, inline results and forwards of shared widgets work on the test server only |
| Forwarding and copying | A sent widget has protected content and cannot be forwarded; `copyMessage` and `copyMessages` refuse it |
| Permissions | `can_send_other_messages` also covers widgets |
| Payments | `TelegramWidget.wallet` with TON Connect `sendTransaction` and `signData`; not for the node |

The widgets page ends with a system prompt for a language model that
writes widgets: Telegram expects widgets to be written by models.

## What it costs the node

- **No HTTP server and no `initData`.** A widget works with polling and
  with the webhook alike. The Mini Apps item in "Not now" waits on pages
  and `initData`; widgets cover part of that need without either.
- **The node's own client** ([ADR-0080](../adr/0080-keep-the-own-telegram-client-and-name-the-library-to-move-to.md))
  needs the five methods and the two updates; no library.
- **A new kind of update.** `widget_query` must enter the inbox like a
  button's press (R-DLV-1, R-DLV-2) and be answered within 15 s, so a
  query the node cannot answer in time needs a design: an answer at once
  and the work after it.
- **A fallback everywhere a widget cannot go:** a group, a forum topic, a
  client that cannot draw it. The plain or rich message (R-DLV-11) stays
  the form that always works.

## Where the node would use it

In the order of value seen today:

1. **A live panel of a module's cells.** A battery's level, a chart of the
   day as inline SVG, the time of the last reading. A module writes a
   cell, the node calls `editMessageWidgetData`: no model call and no
   notification. It fits the model-free pipelines of modules (ADR-0065)
   and replaces a run of `notify` messages with one message that stays
   current.
2. **The card of a background job** (R-LIVE-9): a build of a module or
   `/update` as a progress bar with steps, where `editMessageText` rewrites
   the text today.
3. **A turn's plan as a checklist** with a status on every line. "Not now"
   records that a reaction marks a whole message only; a widget's state
   marks each line, silently.
4. **`/status` and `/modules` as a live summary** of the node, its version
   and its modules' last runs; buttons such as Install and Roll back stay
   ordinary inline buttons beside it.
5. **Charts.** Rich messages draw tables, not charts. A sparkline or bars
   from about a hundred compact points fit in `chat_data`.
6. **Later, with ADR-0017's extension points (P3):** a module declares its
   own widget in its manifest beside its commands and buttons.

Not a fit:

- **Handing a secret to the node from the chat** ("Not now"): a request
  carries 64 bytes, the widget's code is public and QuickJS has no
  WebCrypto. That still needs a page.
- **The family's group:** groups get no widgets in production, and a
  widget cannot be forwarded to them.
- **The pipeline editor of the vision** (a Node-RED-like UI): 600 px and
  32 KB are too small to edit in. Showing a chain as a picture would fit.

## The decision a design must make: who writes the HTML

A widget is code that runs in the owner's client and looks like the
node's interface. That is a security boundary, so it needs an ADR.

| Option | What it is | For | Against |
|---|---|---|---|
| A. Templates in the transport | A fixed set embedded in the binary (gauge, sparkline, checklist, progress); the model and modules give data only | Same rule as the rich blocks' allowlist ([ADR-0054](../adr/0054-structured-replies-as-rich-blocks-the-transport-builds.md)); tested once; size checked at build time | Only the shapes the node ships |
| B. A module's widget | The HTML comes with the module and the owner vouches for it as for its source ([ADR-0068](../adr/0068-vouch-for-module-source-per-file-and-vetted-crates.md)) | A module shows its own data its own way | One more file to vouch for; a module's widget must not look like the node's confirmations |
| C. The model writes it in a turn | The agent sends HTML it wrote | Most flexible; Telegram even ships a prompt for it | A model, above all in a tainted session (R-POL-6), could draw a fake confirmation; it breaks the rule that no button comes from the model's text (R-DLV-11) |

The reading here: start with A; B with the extension points of P3; C
only from a clean session with the owner's confirmation, if ever.

## Not checked

- How each client (iOS, Android, desktop, web) draws a widget today, and
  what an older client shows in its place.
- Whether a `widget_query` arrives by the webhook with the same
  `allowed_updates` the node sets now.
- The size of a real template after zlib, against the 4,096-byte limit.
