# ADR-0078: Receive Telegram updates by polling or by a webhook, chosen in the node's file

- **Status:** Accepted
- **Date:** 2026-11-13
- **Deciders:** Igor Artasevych
- **Spec:** [7.1 Guaranteed delivery](../spec.md#71-guaranteed-delivery)
  (R-DLV-1, R-DLV-2, R-DLV-8),
  [7.14 Node configuration](../spec.md#714-node-configuration),
  [11 Open decisions](../spec.md#11-open-decisions),
  [14 Not now](../spec.md#14-not-now)

## Context

The node receives Telegram updates by long polling and has no public
listener ([ADR-0018](0018-own-telegram-bot-api-client.md)). On 2026-11-13
the owner asked for a webhook as a second mode of the transport: a setting
chooses between them, the agent can change it, and on Railway the node
registers the webhook on its public domain (board card G-152). The details
and the checked facts are in the
[design note](../design/telegram-webhook-design.md).

What bounds the decision:

- Telegram allows one way at a time: `getUpdates` does not work while a
  webhook is set. Undelivered updates wait up to 24 hours in either mode.
- A webhook needs a public HTTPS address on port 443, 80, 88 or 8443. A node
  at home behind NAT has none, and the project's nodes are meant to run
  there too.
- An update is confirmed only after it is in the inbox (R-DLV-2), and a
  repeat is rejected by `idemKey` (R-DLV-1).
- Two processes must not poll one token (R-DLV-8). On Railway this already
  holds: a service with a volume never has two active deployments, and the
  database lock stops a second core
  ([ADR-0019](0019-release-leases-at-core-start.md)). Polling loses no
  update today.
- The spec keeps "an HTTP server" in "Not now", beside Telegram Mini Apps.
  The secret intake page (G-149) will need a public listener as well.
- The metrics endpoint is an HTTP listener on the loopback address with no
  authentication.

## Options

### A. Long polling only

Nothing changes. The node keeps reaching out; R-DLV-8 stays a rule about
deploys.

### B. Webhook only

The node always runs a public listener and registers a webhook at start.
Polling is removed from the transport.

### C. Both, chosen in `ganglion.yaml`

`telegram.updates: polling | webhook`, polling by default. A webhook needs
the node's public listener (`http.addr`, `http.public_url`). The node
registers the webhook itself at start, checks first that its address
reaches it, and removes its own webhook when the file says polling again.

### D. Both, chosen by the node

The node looks for a public address (`RAILWAY_PUBLIC_DOMAIN`, a probe) and
takes the webhook when it finds one, polling otherwise.

## Comparison

| Criterion | A. Polling only | B. Webhook only | C. Chosen in the file | D. Chosen by the node |
|---|---|---|---|---|
| A node behind NAT | Works | Does not work | Works (polling) | Works |
| What the owner asked for | No | Half: no setting | Yes | No setting to change |
| Loss of updates on a deploy | None | None; a delay until Telegram retries | Same as the chosen mode | Same |
| Public entry to the node | None | Always | Only when the file opens it | Opens by itself |
| A wrong address | Cannot happen | Cuts the owner off | Guarded: the node checks its address and falls back | Guarded, but the mode is a surprise |
| Listener for the G-149 page | Still to decide | There | There, when on | There, sometimes |
| Code and tests | None | A listener; polling removed | A listener and a second receiving path beside polling | The same plus detection |
| Fit with R-CFG-1 (the file is the source) | Yes | Yes | Yes | No |

## Decision

**C** (accepted by the owner, 2026-11-13). Polling must stay for nodes without a public address, so B
is out; the owner asked for a setting, so A is out; D opens a public entry
without anybody writing it down. With C the mode is one field, polling
remains the default, and a public listener exists only on a node whose file
asks for one.

Inside C, as the design note describes it:

- The listener is a component of the node, not of the Telegram transport,
  on its own address and apart from the metrics listener. It serves the
  webhook and a health route and nothing else. This takes the listener out
  of "Not now" in that narrow form; Mini Apps, pages, an HTTP API and a web
  UI stay there. The G-149 intake page becomes a route on it under its own
  ADR.
- Telegram's requests are verified by the `X-Telegram-Bot-Api-Secret-Token`
  header. The secret is derived from the bot token by HMAC, so there is
  nothing to configure or store.
- A webhook update takes the path a polled one takes: the same event, the
  same `idemKey`, the same sink. `200` goes out only after the inbox has the
  update; a failed write is `500` and Telegram sends it again.
- `setWebhook` at every start, with `max_connections: 1` to keep the order
  and without dropping pending updates. No `deleteWebhook` at a stop.
- In polling mode the node deletes only a webhook it recorded setting
  itself. Another process's webhook is today's 409 alert.
- The mode changes at a restart (R-CFG-6). The agent changes it through
  M6's `config.propose`, on a card of its own.
- Two guards against a lock-out: the node checks its public address before
  it registers, and returns to polling for the run when Telegram reports
  failed deliveries.

## Security

This is the node's first public entry, so it is a new trust boundary: the
internet to the listener.

- **Trust boundaries:** the listener, behind a proxy that ends TLS. Telegram
  and the platform are trusted as they are today: one carries every message,
  the other runs the process.
- **What an attacker controls:** any request to the public address, its
  headers and body; a web page or a message the agent reads.
- **A forged update in the admin's name:** the secret header, compared in
  constant time before the body is parsed. The secret is in no file, URL or
  log, and the bot token cannot be computed from it.
- **A replayed update:** the unique index on `idemKey`.
- **Large or slow requests:** a body cap, read timeouts, a cap on requests
  in flight.
- **Other routes:** none exist; the metrics stay on the loopback listener.
- **The agent steered into opening the listener or pointing it elsewhere:**
  the change needs the owner's approval on a separate card, and a tainted
  session cannot propose it.
- **The source address** is not used as a check: behind a proxy it is a
  header a client can write.

## Consequences

- A node with a public address stops holding a long-poll connection, and a
  listener exists for the G-149 page.
- Two receiving paths must stay equal: every transport test of an incoming
  update has to hold in both modes, and chatlab's fake Bot API gains
  `setWebhook`, `deleteWebhook`, `getWebhookInfo` and a way to post updates.
- After a restart in webhook mode an update arrives when Telegram retries,
  on a schedule Telegram does not document. Polling fetches at once.
- A public entry to keep safe, where there was none.
- To build: the listener package and its config section, the webhook
  handler, the registration and the record of it, the two guards, `/status`
  lines for the mode, the schema, the example file, the Railway runbook, the
  code map and the container diagram, and requirements in spec 7.1 and 7.14.
- Before the code: a spike on Railway with the test bot (redelivery delay,
  the node reaching its own domain, the target port, the largest update).

## Revisit when

- The spike shows that Telegram redelivers slowly after a restart, or that
  the node cannot reach its own public address on Railway.
- A second transport needs a webhook: its handler joins the same listener.
- The node gets its own TLS or sits on a host with no proxy in front.
