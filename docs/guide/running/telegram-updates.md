# Telegram updates: polling or webhook

Telegram can deliver messages to the node in two ways.

## Polling (the default)

The node asks Telegram for new messages. Nothing has to be reachable from outside, so it works behind a home router. This is the default (`telegram.updates: polling`) and needs no further setup.

## Webhook

Telegram calls your node. It is faster to react and uses no idle requests, but the node needs a public HTTPS address. Three settings together:

```yaml
http:
  addr: ":8080"
  public_url: https://example.com
telegram:
  updates: webhook
```

- `http.addr` is the port the node listens on. It speaks plain HTTP: put a proxy that ends TLS in front of it, or use a platform that does so.
- `http.public_url` is the HTTPS address Telegram reaches it at, with no path and no port or one of 443, 80, 88, 8443.
- `telegram.updates: webhook` switches the mode.

Restart the node. If the listener or `public_url` is missing, the node refuses to start. At every start, the node checks that its own address reaches it, takes the messages that are waiting by polling once, and registers the webhook. It accepts only requests that carry Telegram's secret header. The log says `telegram: webhook registered` and `/status` shows `Telegram: webhook`.

The listener answers only the webhook's path and `/healthz`. The metrics endpoint is never on it.

## If the webhook breaks

If the public address does not reach the node, or deliveries keep failing, the node tells the admin in the chat and falls back to polling until the next restart. `/status` warns. If another process sets or removes the bot's webhook, the node tells the admin and stops receiving until you stop that process and restart. A wrong address never cuts the chat off. To go back to polling for good, set `telegram.updates: polling` and restart. The node removes the webhook it set.

## One poller per token

Only one process may poll a bot token. A second process gets a `409 Conflict` from Telegram, and one of the two stops receiving messages. This happens most often when you run a test node on the same bot as your real one. Use a separate bot for testing, or stop the other process first.
