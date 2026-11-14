# Configure

All settings live in one file, `ganglion.yaml`, beside the database. The repository ships an annotated copy with every default.

```sh
cp ganglion.example.yaml ganglion.yaml
```

Every key is optional. An unknown key stops the node at start and the error names the key, so a typo never goes unnoticed. If you start the node with `--config`, that file must exist. Without it, the node looks for `ganglion.yaml` beside the database and uses defaults when it is missing.

## The minimum

Set three things.

**1. The bot token.** The default already reads it from an environment variable:

```yaml
telegram:
  token: env:TELEGRAM_BOT_TOKEN
```

**2. The admin: you.** Use your numeric Telegram user id:

```yaml
telegram:
  admin: telegram:user:123456
```

Without an admin nobody can use the bot.

**3. A model.** Without a `models` section the agent is off and the bot only echoes. The default example uses the `claude` CLI:

```yaml
models:
  providers:
    - {name: claude, type: claude-cli}
  models:
    - {id: sonnet, provider: claude, name: sonnet, tools: true, context: 200000}
    - {id: haiku, provider: claude, name: haiku, tools: true, context: 200000}
  purposes:
    chat: [{model: sonnet, effort: medium}, {model: haiku}]
```

The `chat` purpose is required. See [Models](../running/models.md) for more.

To check that Telegram works before you set up a model, run a node with only a token and an admin. It echoes your messages.

## Secrets

A secret is never written in the file. A field that takes one holds a reference:

- `env:NAME` reads the environment variable `NAME`, or the file named by `NAME_FILE`.
- `store:NAME` reads a secret from the node's own encrypted store.

A literal value in such a field stops the node at start.

Manage the store with the `ganglion secret` command. Run it as the node's user, against the same database. The value is read from standard input, never from an argument:

```sh
printf %s "$TOKEN" | ganglion secret set --db ganglion.db TELEGRAM_BOT_TOKEN
ganglion secret list --db ganglion.db
ganglion secret rm --db ganglion.db TELEGRAM_BOT_TOKEN
```

Then point the file at it with `token: store:TELEGRAM_BOT_TOKEN`. `list` shows names, dates and owners, never values. A module's own secrets use `--module <name>`. Values from `store:` are read at start, so restart the node after changing one.

Never paste a secret into the Telegram chat. There is no safe chat-based way to hand one over yet.

## The master key

The store is encrypted with a key. By default the node creates `master.key` beside the database at its first start (mode `0600`). Or you can supply the key yourself in `GANGLION_MASTER_KEY` (base64 of 32 random bytes, made with `openssl rand -base64 32`) or in a file named by `GANGLION_MASTER_KEY_FILE`.

Keep the key. A database without its key yields no secrets. If the key does not fit the database, the node still starts, but the store is locked and every stored secret reads as unset. `/status` says so. Give the node the old key back, or send `/secrets reset` to drop the stored secrets and enter them again.

## Where the data goes

The data directory is the folder that holds the database. By default:

| What | Where |
|---|---|
| Database | `ganglion.db` (plus `-wal` and `-shm` files while running) |
| Configuration | `ganglion.yaml` |
| Master key | `master.key` |
| Logs | `logs/` |
| Persona, memory, module sources | `workspace/` |
| Installed module files | `modules/` |

`workspace/` is the `agent.workspace` setting, relative to the folder the node starts in. It sits beside the database when you start the node from the data directory, as the image does.

Pick the database path with `--db` or `GANGLION_DB`, and the configuration with `--config` or `GANGLION_CONFIG`. In the container image everything is under `/data`.

Next: [your first conversation](first-conversation.md). Every setting is listed in the [configuration reference](../running/configuration.md).
