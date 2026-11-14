# Deploying on Railway

This page shows how to run a node on [Railway](https://railway.com). Most of it applies to any container platform. The image is built from the `Dockerfile` in the repository.

## Before you start

- Use a bot token that no other process polls. A second poller causes a 409 error (see [Telegram updates](telegram-updates.md#one-poller-per-token)).
- Decide how the agent reaches a model: a `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`, or an API key such as `OPENROUTER_API_KEY`. See [Models](models.md).
- Railway containers share the host's kernel. Module builds need Linux 6.2 or later. You can check after the first start.

## The image

The image is about 1.2 GB, mostly the Rust toolchain. It runs as the user `ganglion` (uid 10001) and starts `ganglion run --db /data/ganglion.db`. Data lives in `/data`. The `claude` CLI is included. Build without it with `--build-arg CLAUDE_CLI=off`.

## Create the service

1. Create a new service from the repository. Railway builds the `Dockerfile` by itself. With the CLI: `railway add -s <service>`, then `railway up -s <service>`. Always name the service in CLI commands, so you do not act on another service by accident.
2. **Add a volume** mounted at `/data`. The image has no `VOLUME` line on purpose, because Railway refuses one.
3. **Leave the user alone.** Railway mounts volumes as root. Do not set `RAILWAY_RUN_UID=0`: the sandbox and module builds are off for root. The image starts as root only to hand `/data` to uid 10001 and then drops to that user. Check with `ls -ld /data` (owner `ganglion`).
4. **Set variables:**

   | Variable | Value |
   |---|---|
   | `TELEGRAM_BOT_TOKEN` | your bot's token |
   | `CLAUDE_CODE_OAUTH_TOKEN` | the token from `claude setup-token`, if you use the `claude` CLI |
   | `OPENROUTER_API_KEY` | a provider key, if you use that service |

   A sealed variable resolves to an empty value when another variable refers to it, so set those by value.
5. **Memory:** give the service about 1 GB while module builds run. Set `node.memory_limit` in the file (for example `100MiB`) to cap the node itself. Builds are separate processes outside that limit.
6. **Restart policy:** "on failure". The node exits with code 75 for `/restart` and the platform starts it again.

`railway ssh` needs an SSH key registered in your Railway account (`railway ssh keys add`).

## Create `ganglion.yaml` in the volume

The file is `/data/ganglion.yaml`. It must be a file inside the volume, not a single-file mount, because the node rewrites it. Create it as the node's user, so the node can rewrite it. A file made by root is root's.

```sh
railway ssh
setpriv --reuid=10001 --regid=10001 --clear-groups sh -c 'cat > /data/ganglion.yaml'
```

Paste the first copy, then press Ctrl-D:

```yaml
node:
  memory_limit: 100MiB
  tz: Europe/London
telegram:
  token: env:TELEGRAM_BOT_TOKEN
  admin: telegram:user:123456
```

Add a `models` section for your provider. See [Configuration](../getting-started/configure.md).

## Secrets

Set a module's secret over `railway ssh`, as the node's user, with the value on standard input:

```sh
printf %s "$VALUE" | setpriv --reuid=10001 --regid=10001 --clear-groups \
  ganglion secret set --db /data/ganglion.db --module <module> NAME
```

The module reads it at its next run with no restart. The key is `/data/master.key`, made at first start. Back it up with the database, or supply your own `GANGLION_MASTER_KEY` as a sealed variable before you store the first secret. See [Configure](../getting-started/configure.md#the-master-key).

## Telegram by webhook (optional)

Polling needs nothing more. To use a webhook:

1. Generate a domain for the service (Settings, Networking, or `railway domain -s <service>`). Prefer one that cannot be guessed from the service's name, and do not publish it: anyone can send requests to a public address.
2. Add to `/data/ganglion.yaml`:

   ```yaml
   http:
     addr: ":8080"
     public_url: https://example.com
   telegram:
     updates: webhook
   ```

   Use the port Railway gives the service (`PORT`) and your real domain.
3. Restart. `/status` shows `Telegram: webhook`.

To go back, set `telegram.updates: polling` and restart. See [Telegram updates](telegram-updates.md).

## Check after the first start

```sh
railway ssh
uname -r                                  # 6.2 or later
ls -ld /data                              # owner ganglion
setpriv --reuid=10001 --regid=10001 --clear-groups ganglion mcp --db /data/ganglion.db
```

The log must say `sandbox: ready`. If it says `sandbox: commands are off`, the reason names what is missing and nothing can be built. Then send `/status` in your private chat.

Run every command that touches `/data` as uid 10001. A command run as root leaves root-owned files (the database's `-wal` and `-shm`) that the node cannot open at its next start.

## Deploying from a git worktree

`railway up` uploads the main checkout, not a worktree. Name the folder: `railway up <path> --path-as-root -s <service>`.
