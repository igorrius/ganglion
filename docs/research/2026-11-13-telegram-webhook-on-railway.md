# Telegram webhook on Railway: what the spike measured

- **Date:** 2026-11-13
- **Task:** P1 · M6 6.6 ([design note](../plans/telegram-webhook-design.md),
  [ADR-0078](../adr/0078-telegram-updates-by-polling-or-webhook.md))
- **Question:** how fast does Telegram redeliver after the node was away, does the node reach its own
  public address from inside Railway, which port and variables does Railway give a service, and how
  large is an update?
- **Status:** measured on Railway with the test bot and closed: the webhook is removed from the test
  bot and the `webhook-spike` service is deleted. **A 30-minute stop is not measured.**

## Method

`tools/webhookspike` ran as its own service, `webhook-spike`, in the project that holds the node, with
the test bot's token and no volume. At start it logs its environment, asks its public address for a
value it made for this start, registers the webhook (the secret derived from the token,
`max_connections: 1`) and logs every delivery. `/down N` makes it answer `503` for N seconds; a real
stop was `railway down`, then a new deploy. The owner sent the messages.

## Results

**Railway.**

| What | Found |
|---|---|
| `PORT` | `8080`, set even when the service has no domain |
| `RAILWAY_PUBLIC_DOMAIN` | Empty until a domain is generated; a running container does not get it, the next deploy does |
| Generated domain | `<service>-<environment>.up.railway.app`; no target port had to be set, the domain found the port the process listens on |
| The node asking its own address | Works from inside the container, 12–45 ms |
| The address during a deploy (no volume) | The first request of the new process was answered `200` by the **old** deployment; 3 s later by the new one |
| The address with no deployment | `404` from Railway |
| Headers on a delivery | `X-Forwarded-For`, `X-Real-Ip`, `X-Forwarded-Proto: https` |

**Telegram.**

| What | Found |
|---|---|
| A delivery when all is well | 0.2–0.8 s after the message was sent |
| Retries after a refusal | At once, then 2, 4, 8, 16, 32, 64 s after the first attempt, then every 1–2 minutes (seen: 127 s, 236 s, 301 s) |
| `/down 60` | Refused at 0, 0, 2, 6, 14, 30 s; delivered at 62 s |
| `/down 300` | Refused at 1, 1, 3, 7, 15, 31, 63, 127, 236 s; delivered at 301 s |
| A real stop of about 4 minutes | `getWebhookInfo` at the next start: `pending_update_count: 1`, `last_error_message: "Wrong response from the webhook: 404 Not Found"`. The update came **93 s after `setWebhook`** (96 s after the process start, 235 s after it was sent) |
| `setWebhook` with the same address | Does not make Telegram deliver at once: the update waited for the next retry |
| Size of an update | 381–414 bytes for a short message; **18,103 bytes** for a message of 4,096 Cyrillic characters, delivered in 0.7 s |

## Conclusions

1. **A plain "does my address answer" check is not enough.** During a deploy the address answered
   `200` from the old process. The check must compare a value made for this start, as the design note
   says; a few tries a few seconds apart are needed.
2. **Nothing is lost while the node is away**, on a `503` from the node or a `404` from Railway.
3. **After a restart a waiting update can take up to about two minutes** to come by the webhook, where
   polling fetches it at once. The node restarts on every deploy and on `/restart`, so this would be
   felt. The design note's fallback therefore becomes the rule: when `getWebhookInfo` at start shows
   updates waiting, the node removes the webhook, takes them with `getUpdates` until none is left, and
   registers the webhook again. Not tried in the spike; to be checked with the first live run.
4. **`RAILWAY_PUBLIC_DOMAIN` is a usable source of the address** on Railway once a domain exists, and
   `PORT` of the listener's port. The file stays the one source (the owner accepted that with the ADR);
   this is recorded for the template install.
5. No target port needs to be chosen by hand for a generated domain.
6. **The body cap of 1 MiB in the design note stays.** The longest text message was 18 KB: Telegram
   writes a character outside ASCII as `\uXXXX`, six bytes for each of the 4,096 UTF-16 units a
   message may hold, so a text of the full length cannot pass about 25 KB before its entities. The
   cap leaves more than forty times that.

## Not measured

- A stop of 30 minutes, to see that the retries stay at 1–2 minutes and do not stop. It does not
  change the design: at a start the node takes waiting updates by polling (conclusion 3). The first
  live run of task 6.6 can check it.
