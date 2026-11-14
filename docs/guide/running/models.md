# Models

The agent can use several models. You describe them in the `models` section and say which one does what.

## Providers

A provider is where a model comes from. There are two types.

- **`claude-cli`**: an unmodified `claude` command-line tool, installed and logged in on the machine, using your own subscription. No key goes in the configuration. An optional `command` names the binary.
- **`openai`**: any OpenAI-compatible Chat Completions service: OpenAI, OpenRouter, a local Ollama. It needs `base_url`, and `key` unless the address is local (`localhost`, `127.0.0.1`).

```yaml
models:
  providers:
    - {name: claude, type: claude-cli}
    - {name: openrouter, type: openai, base_url: https://openrouter.ai/api/v1, key: env:OPENROUTER_API_KEY}
```

## Models

Each model entry says what the model is:

```yaml
  models:
    - {id: sonnet, provider: claude, name: sonnet, tools: true, context: 200000}
    - {id: flash, provider: openrouter, name: deepseek/deepseek-v4.1-flash, tools: true, context: 1048576, price_in: 0.045, price_out: 0.5}
```

- `id` is the short name that purposes use. It defaults to `name`.
- `name` is the provider's own name for the model.
- `tools` says the model can call tools. `vision` says it reads images. `local` says the data stays on your hardware.
- `context` is the window in tokens.
- `price_in` and `price_out` are US dollars per million tokens. Use 0 for subscription and local models.

## Purposes and fallback

A purpose is a job. Each lists its models in the order they are tried. If a model is out of rate limits or fails, the next one is used.

| Purpose | What it does | If not set |
|---|---|---|
| `chat` | A conversation turn. **Required.** | |
| `check` | Quick yes/no checks in a clean context. | Uses `chat`'s list. |
| `compact` | Summaries of long history. | Uses `chat`'s list. |
| `translate` | The bot's fixed texts in a new language. | Uses `chat`'s list. |
| `memory` | The memory review. | Uses `check`'s list. |
| `code` | Writing modules and skills. | No fallback. |

```yaml
  purposes:
    chat:    [{model: sonnet, effort: medium}, {model: haiku}]
    check:   [{model: haiku, effort: low}]
    code:    [{model: opus, effort: high}, {model: sonnet, effort: high}]
```

`effort` is `low`, `medium` or `high`, and also `xhigh` or `max` on the `claude-cli` route.

## Logging in the claude CLI

On your own machine, log in with `claude` as you normally would, as the user that runs the node. ganglion never sees the login.

On a server or in a container, where you cannot log in interactively, run this on a machine where you can:

```sh
claude setup-token
```

It prints a token. Set it as `CLAUDE_CODE_OAUTH_TOKEN` in the node's environment. The `claude-cli` provider passes it to the CLI. Commands the agent runs do not get it.

A login through the chat is not built yet. Use your own subscription for your own node only. Taking a subscription token and calling the API with it directly is not supported.

## Limits

Spending is bounded per day, in the node's time zone (`node.tz`).

- `agent.limits.daily_turns` (default 300) and `daily_tokens` (default 1,000,000) apply per person. Past either, the bot answers "Daily limit reached; the bot answers again tomorrow." and alerts the admin. Scheduled tasks count too.
- `agent.limits.daily_budget_usd` (default 1) caps spend. Past it, a turn uses only free and local models (price 0). Background housekeeping calls are not cut. Models on a subscription cost 0, so the budget does not limit them unless you set `price_in` and `price_out`.

`/status` shows today's turns and tokens against the limits and what was spent. Set a limit to `0` or `off` to remove it.
