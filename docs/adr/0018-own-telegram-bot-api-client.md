# ADR-0018: Use our own minimal Telegram Bot API client

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-2, R-DLV-5, R-DLV-8), principles 7 and 8; P1 decision D-2

## Context

M2 builds the Telegram transport. It needs only a few Bot API methods:
`getMe`, `getUpdates` (long polling), `sendMessage` with forum topics
(`message_thread_id`) and silent messages (`disable_notification`). Later
milestones add a few more (inline keyboards, `answerCallbackQuery`,
`setMyCommands`; [ADR-0017](0017-extension-points.md)).

What the transport must guarantee:

- **R-DLV-2:** the update offset advances only after the update is stored in
  the inbox. The offset is confirmed to Telegram by the next `getUpdates`
  call, so whoever runs the polling loop owns this guarantee.
- **R-DLV-8:** `409 Conflict` (another poller, or a webhook) stops polling
  and alerts; it must not be retried in a loop that fights the other
  process.
- **R-DLV-5:** `429` with `retry_after` is honoured; a long Telegram outage
  backs off without a busy loop.
- The bot token is part of every request URL. It must never appear in logs
  or errors.
- Principle 8: few dependencies, small memory.

Facts from the Go module proxy and source, 2026-11-02:

| Library | Latest | Dependencies | Polling loop |
|---|---|---|---|
| `github.com/go-telegram/bot` | v1.27.0, 2026-09-11 | none | Stores the update id as the offset when it reads the update, before handlers run (`get_updates.go`); a `409` is logged and retried with backoff |
| `github.com/mymmrac/telego` | v1.12.1, 2026-08-29 | fasthttp, sonic, fastjson, brotli, klauspost/compress and more | Not checked: the dependency set already rules it out |
| `github.com/go-telegram-bot-api/telegram-bot-api/v5` | v5.5.1, 2021-12-13 | — | Unmaintained |
| `gopkg.in/telebot.v4` | v4.0.0-beta.10, 2026-06-16 | — | A framework (router, middleware); still beta |

## Options

### A. Own minimal client

`net/http` and `encoding/json`: one `call(method, params, result)` function
that decodes the Bot API envelope (`ok`, `error_code`, `description`,
`parameters.retry_after`), plus typed wrappers for the methods we use. Our
transport owns the polling loop.

### B. `go-telegram/bot`, raw methods only

Use its typed methods and models, but not its polling loop, which breaks
R-DLV-2 and R-DLV-8 as shown above. Our transport calls `GetUpdates`
itself.

### C. A framework (`telebot`, `telego`)

Handlers, routing and middleware out of the box.

## Comparison

| Criterion | A. Own | B. go-telegram/bot, raw | C. Framework |
|---|---|---|---|
| R-DLV-2 offset after the inbox write | Ours by construction | Ours, but only if we never use the library's loop | Framework loop; would need to be bypassed |
| R-DLV-8 stop on `409` | Ours | Ours (its error is a wrapped `ErrorConflict`) | Framework behaviour |
| Token redaction in errors | One place, ours | Must wrap every error the library returns | Same as B |
| Dependencies | None | None | Many (telego), or a beta framework |
| Code to write | ~300 lines with tests | ~100 lines of glue | Glue, plus working around the framework |
| New Bot API features | Add a method when needed | Usually there already | Usually there already |
| Fit with ADR-0017 (Telegram types stay inside the transport) | Types are ours and private | Library types inside the package | Framework types leak into handlers |

## Decision

**A.** The two guarantees that matter most (offset after the inbox write,
stop on `409`) live in the polling loop, and the only zero-dependency
library gets both wrong in its own loop. Without that loop, what is left
from B is typed wrappers for five methods, which do not justify a
dependency.

## Consequences

- `transport/telegram` holds the client, the polling loop and the send
  path; Bot API types are unexported and do not leave the package.
- A fake Bot API (`transport/telegram/telegramtest`) is part of the work: it
  carries the offset, long polling, `409` and `429` semantics that the tests
  and the crash harness rely on.
- Every error the client returns is built by us, with the token replaced by
  `<token>`; a test checks that.
- New Bot API features cost us a method each; the Bot API's JSON is stable
  and additive.

## Revisit when

We need a large part of the Bot API (payments, media groups, inline mode,
business accounts), or a library offers a polling hook that confirms the
offset only after our sink returns and stops on `409`.
