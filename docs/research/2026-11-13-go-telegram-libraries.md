# Go libraries for the Telegram Bot API, read again

- **Date:** 2026-11-13
- **For:** [ADR-0080](../adr/0080-keep-the-own-telegram-client-and-name-the-library-to-move-to.md),
  which looks again at [ADR-0018](../adr/0018-own-telegram-bot-api-client.md)
- **Question:** the owner asked whether a mainstream Go library would make the Telegram integration
  easier than the node's own client. What do the libraries give today, and do they keep what the
  node's delivery rules need?
- **Status:** read from the Go module proxy and the modules' source on 2026-11-13. Nothing was run.

## Method

`go list -m <module>@latest` for the version and its date, then `go mod download` and the source of
the three libraries that are alive. In each: the dependencies in `go.mod`, the polling loop, the
webhook handler, how an error is built, and whether the methods the node calls are there,
`sendRichMessage` among them (Bot API 10, [ADR-0054](../adr/0054-structured-replies-as-rich-blocks-the-transport-builds.md)).

## The libraries

| Library | Latest | Dependencies | State |
|---|---|---|---|
| `github.com/go-telegram/bot` | v1.28.0, 2026-11-09 | none | Alive; says Bot API 10.4, released the day 10.4 came out |
| `github.com/PaulSonOfLars/gotgbot/v2` | v2.0.0-rc.37, 2026-10-17 | none | Alive; generated from the Bot API's specification; a release candidate, as every v2 version is |
| `github.com/mymmrac/telego` | v1.13.0, 2026-11-11 | fasthttp, sonic, go-json, fastjson and their own | Alive |
| `gopkg.in/telebot.v4` | v4.0.0-beta.10, 2026-06-16 | not read | A framework, still beta, five months without a version |
| `github.com/go-telegram-bot-api/telegram-bot-api/v5` | v5.5.1, 2021-12-13 | not read | Not maintained |

ADR-0018 did not look at `gotgbot`.

## What the node needs, and what each gives

| | `go-telegram/bot` | `gotgbot` | `telego` |
|---|---|---|---|
| Methods | 191 | 370 | not counted |
| `sendRichMessage` | Yes | Yes | Yes |
| `getUpdates` as a method the caller can call | **No**: only its own loop calls it, and no raw request is exported | Yes | Yes |
| Its polling loop: the offset | Stored when the update is read, before a handler runs | Sent on from the last update of the batch, before the updates are handled | not read |
| Its polling loop: a `409` | Logged and asked again after a wait | not read | not read |
| Its webhook handler: the secret | Compared with `!=`; a wrong one still gets `200` | Compared with `!=`; a wrong one gets `401` | not read |
| Its webhook handler: the answer | `200` as soon as the update is put on a channel, before any handler | not read to the end | not read |
| The token in errors | Replaced in the URL of a network error | Replaced in the URL of a network error | not read |
| An error's code and `retry_after` | Sentinel errors (`ErrorConflict` and others), a typed error for 429 | `TelegramError` with the code and the response parameters | not read |
| The HTTP client can be ours | An option | Yes: `BotClient` is an interface of one request method | not read |

## What the node's own client is

On `master`: `transport/telegram/client.go` (289 lines) and `types.go` (188 lines), ten methods; the
webhook mode adds three. The rest of the package, about 3,200 lines without tests, is behaviour no
library has: Markdown to entities, splitting, rich messages built from an allowlist, reactions, the
addressing rule of groups, the health for `/status`, the webhook's start and its two guards.

Every call is traced and timed in one place (`ganglion.telegram.api.duration`), reads at most 10 MiB
of an answer, and returns an error the client built itself with the token replaced.

## Conclusions

1. **No library's own loop can receive for the node**, by polling or by webhook. Each confirms an
   update before the node has stored it, which is what R-DLV-2 forbids, and the webhook handlers do
   not compare the secret in constant time. This is what ADR-0018 found for polling; it holds for
   the webhook too.
2. **`go-telegram/bot` cannot be used for methods only**: it exports no `getUpdates`. ADR-0018's
   option B assumed it could.
3. **`gotgbot` can.** It has every method as a plain call, no dependencies, an error with the code,
   and a client interface the node could implement to keep its own tracing, limits and redaction. It
   is generated from the specification, so a new Bot API method arrives without anybody writing it.
4. **What a library would replace is small**: the two files above, about 480 of the package's 3,600
   lines. What it would add is a package of 29,000 lines, most of them generated (how much of it the
   linker keeps was not measured), and a dependency whose versions are all release candidates.
5. **Where a library pays** is breadth: downloading files, media groups, payments, inline mode. The
   node uses none of them today.

## Not checked

- `telego` and `telebot` past their dependencies and state.
- The size of the binary and the memory with `gotgbot` linked in.
- Whether `gotgbot`'s generated types decode everything the node reads today (rich messages that
  come to the bot, forum topics) the way the node's own types do.
