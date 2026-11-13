# ADR-0080: Keep the own Telegram client, and name the library to move to

- **Status:** Proposed
- **Date:** 2026-11-13
- **Deciders:** Igor Artasevych
- **Spec:** [7.1 Guaranteed delivery](../spec.md#71-guaranteed-delivery)
  (R-DLV-1, R-DLV-2, R-DLV-8, R-DLV-18, R-DLV-19), principle 8

## Context

On 2026-11-13, while the webhook mode was checked live, the owner asked to
look again at [ADR-0018](0018-own-telegram-bot-api-client.md): would a
mainstream Go library not make the Telegram integration much easier than
the node's own client?

ADR-0018 chose the own client eleven days earlier, for five methods. Since
then:

- The client has ten methods, thirteen with the webhook mode. It is two
  files, about 480 lines; the Telegram package is 3,600.
- The node sends rich messages (`sendRichMessage`, Bot API 10), which a
  library has to know.
- The libraries moved: `go-telegram/bot` follows Bot API 10.4, and
  `gotgbot`, which ADR-0018 did not look at, has no dependencies and is
  generated from the Bot API's specification.

What bounds the decision is unchanged:

- An update is confirmed to Telegram only after it is in the inbox: the
  offset in polling (R-DLV-2), the `200` in webhook mode (R-DLV-18).
- A `409` stops receiving and is not fought (R-DLV-8).
- The bot token is in every request's address and must be in no log or
  error.
- Few dependencies and a small memory (principle 8).

The facts are in the
[research note](../research/2026-11-13-go-telegram-libraries.md), read from
the libraries' source the same day.

## Options

### A. Keep the own client

Nothing changes. A new Bot API method is a few lines when it is needed.

### B. `gotgbot` for the methods and the types, our own receiving

The library's `Bot` replaces `client.go` and `types.go`: every method is
there as a plain call. The node keeps its polling loop, its webhook handler
and the start of the webhook mode, and gives the library its own HTTP
client through the library's `BotClient` interface, so tracing, the limit
on an answer and the redaction stay in one place.

### C. `go-telegram/bot` for sending, our own receiving

The most used library without dependencies. It exports no `getUpdates` and
no raw request, so the node would keep its own client for receiving and use
the library beside it for sending.

### D. A library's own loop or a framework

`go-telegram/bot`'s `Start`, `gotgbot`'s `ext`, `telego`, `telebot`:
handlers, routing, polling and a webhook handler from the library.

## Comparison

| Criterion | A. Own client | B. `gotgbot`, methods only | C. `go-telegram/bot`, sending only | D. A library's loop |
|---|---|---|---|---|
| Confirm only after the inbox (R-DLV-2, R-DLV-18) | Ours | Ours: the loop stays ours | Ours | Broken: every loop read confirms first |
| Stop on `409` (R-DLV-8) | Ours | Ours | Ours | `go-telegram/bot` asks again |
| The webhook's secret in constant time | Ours | Ours: the handler stays ours | Ours | Compared with `!=` in both libraries read |
| The token out of errors | One place, tested | Our `BotClient` keeps the place; the library's errors need a look | Two clients, two places | The library's |
| Dependencies | None | One, no transitive ones; release candidates only | One, no transitive ones | One or many |
| What it replaces | — | About 480 lines of 3,600 | Less: receiving stays | The same, plus our loop, which we cannot give up |
| What it costs now | Nothing | Rewrite every call and the tests that read our types; check that its types read what ours read | The same for sending, and two clients to keep | — |
| A new Bot API method | A few lines each | Already there | Already there for sending | Already there |
| A wide part of the API (files, media groups, payments, inline) | Each written by hand | There | There | There |
| Size and memory | Smallest | A package of 29,000 lines; not measured | Not measured | Not measured |

## Decision

**A now, and B is the way out when the node needs breadth.**

The integration is hard where no library helps. The transport's work is
what happens around a call: when an update counts as received, what a `409`
means, Markdown to entities, splitting, the rule of groups, the webhook's
start and its guards. The webhook mode shows it: both libraries read have a
webhook handler, and neither could be used, because each answers Telegram
before the node has stored the update. A library would have replaced three
methods of some ten lines each.

So a library today removes 480 lines the node already has and tests, and
costs a rewrite of every call. D is out for the reasons ADR-0018 gave, which
still hold and now cover the webhook. C leaves two clients.

B is the option worth having ready. `gotgbot` gives what ADR-0018's option
B wanted and `go-telegram/bot` cannot: methods without a loop. When the
node starts on a part of the Bot API where writing methods by hand is the
real work, it moves to B, and this ADR is the note of where to.

ADR-0018 stays accepted. This ADR does not replace it: it confirms it and
names the library and the signal.

## Security

No new boundary: nothing changes in the code.

For B, when it comes: a dependency joins the process that holds the bot
token and reads everything Telegram sends. What limits it is that it has no
dependencies of its own, that the node gives it the HTTP client (so the
node's limits, timeouts and redaction apply to every request), and that
receiving, where an attacker's bytes enter, stays the node's code. The
library's errors must be checked for the token and for message text before
they reach a log: `gotgbot`'s `TelegramError` carries the request's
parameters.

## Consequences

- Nothing to build now.
- A new Bot API method stays a small piece of handwork; the count of
  methods is the thing to watch.
- The node keeps a client nobody else maintains: a change of the Bot API's
  behaviour is ours to notice. So far the API has been additive.
- The move to B has its plan in the research note: one library, one
  interface to implement, and the three checks that were not made (size,
  memory, whether its types read what ours read).

## Revisit when

- A task needs a part of the Bot API the node has not touched: downloading
  files, media groups, payments, inline mode, business accounts.
- The own client passes about 25 methods or 1,000 lines.
- `gotgbot` releases a stable v2, or a library gets a way to confirm an
  update only after the caller's sink returned.
