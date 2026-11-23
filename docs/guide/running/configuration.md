# Configuration reference

All settings are in `ganglion.yaml`. Every key is optional and the values below are the defaults. The file is checked strictly: an unknown key stops the node at start and the error names it. For an editor with completion, the example file starts with a line that points to the JSON schema `schemas/node.schema.json`.

Only the module list and the `mcp` section change while the node runs. Every other section applies at the next start: send `/restart` or restart the process.

A field that takes a secret holds `env:NAME` or `store:NAME`. See [Configure](../getting-started/configure.md#secrets). Where a value is a duration, write it like `8s`, `10m` or `6h`. `off` turns a feature off where noted.

## node

| Key | Default | Meaning |
|---|---|---|
| `memory_limit` | none | The soft memory limit of the node (`GOMEMLIMIT`), for example `400MiB`. Without it the node takes about 80 % of its container's limit, or none. A `GOMEMLIMIT` in the environment wins. |
| `tz` | `Europe/Kyiv` | The IANA time zone that decides where a day starts. It also times reminders and daily limits. Change it. |

## log

| Key | Default | Meaning |
|---|---|---|
| `level` | `info` | `debug`, `info`, `warn` or `error`. |
| `dir` | `logs/` beside the database | The directory of the rotated log files. `off` writes no files. |

## metrics

| Key | Default | Meaning |
|---|---|---|
| `addr` | `127.0.0.1:9464` | The Prometheus endpoint, `host:port`. `off` runs none. |

## http

| Key | Default | Meaning |
|---|---|---|
| `addr` | `off` | The node's public listener, `host:port`. It serves plain HTTP behind a proxy that ends TLS and carries the Telegram webhook, a health route and, on the node's own bus, `/bus`, the one path your edges connect to. Nothing passes `/bus` without a valid key issued by this node: a missing, expired, revoked or copied key is refused, as is an oversized message or a flood of connections past 64. It is never the metrics endpoint. |
| `public_url` | none | The HTTPS address the outside world reaches the listener at, with no path and no port or one of 443, 80, 88, 8443. |

See [Telegram updates](telegram-updates.md).

## telegram

| Key | Default | Meaning |
|---|---|---|
| `token` | `env:TELEGRAM_BOT_TOKEN` | The bot token. The transport does not start without it. |
| `updates` | `polling` | `polling` or `webhook`. |
| `api_url` | `https://api.telegram.org` | The Bot API's base address. |
| `admin` | none | The admin, as `telegram:user:<id>`. Missing means nobody can use the bot. |
| `reply` | `auto` | When an answer quotes the message it answers: `auto` (in a group, or when newer messages came after it), `always` or `off`. |
| `rich` | `true` | Turn Markdown into Telegram formatting. |
| `reactions.after` | `8s` | How long a turn runs before the bot first reacts. `off` means never. |
| `reactions.dwell` | `3s` | How long a reaction stays before the next one. |
| `groups` | empty | Group chats whose members may talk to the bot. |

A group entry has `chat` (a negative number), `name` and `role: family`. The node writes the line when you press Allow on the group card:

```yaml
telegram:
  groups:
    - chat: -1001234567890
      name: Family
      role: family
```

## agent

| Key | Default | Meaning |
|---|---|---|
| `workspace` | `workspace` | The directory of the agent's persona and memory. Relative to the working directory. The image uses `/data/workspace`. |
| `languages` | beside the database | The directory of the language files. |
| `session_idle` | `6h` | The silence after which a conversation starts afresh. `off` never does. |
| `call_timeout` | `3m` | How long one model call may take. On the `claude` CLI route one call runs every tool call of its turn. Not longer than `turn_timeout`. |
| `turn_timeout` | `5m` | How long a whole turn may take. |
| `max_steps` | `20` | How many model calls one turn may make, from 1 to 100. Listing a connected server's tools and turning them on does not spend them. |
| `limits.daily_budget_usd` | `1` | Spend per day. See [Models](models.md#limits). |
| `limits.daily_turns` | `300` | Turns per person per day. |
| `limits.daily_tokens` | `1000000` | Tokens per person per day. |
| `search.mode` | `auto` | `auto` uses Brave when a key is set and DuckDuckGo otherwise; `off` gives the agent none. |
| `search.brave_key` | `env:BRAVE_API_KEY` | Key for Brave search. Without one, DuckDuckGo is used. |

A `0` or `off` in a limit turns that one off. `limits: off` turns all of them off.

## sandbox

| Key | Default | Meaning |
|---|---|---|
| `deny` | empty | Extra paths the command sandbox refuses, on top of its built-in list. |
| `hide_names` | `false` | Hide the names of denied paths too. |

## models

The providers, models and purposes. Without this section the agent is off. See [Models](models.md).

## destinations

Names for the conversations a message may be sent to: a group, a forum topic or a private chat. The node writes a line when you ask it in your private chat to name a place.

```yaml
destinations:
  - name: family-kitchen
    conv: telegram:-1001234567890/45
```

A name is lowercase letters, digits and dashes, starts with a letter, at most 32 characters, and is not `owner`.

## modules

The modules the node runs and their settings. Installing a module writes to this list.

| Key | Meaning |
|---|---|
| `name` | The module's name. |
| `config` | Its settings, checked against what the module declares. |
| `off` | Set to turn the module off. |
| `notify_to` | Where its messages go: `owner` (your private chat, the default) or names from `destinations`. Up to 8, each message goes to every one. |

```yaml
modules:
  - name: local/price-watch
    notify_to: [family-kitchen, owner]
```

## mcp

The remote MCP servers the node uses. The section changes only through the assistant's tools: `config.propose` refuses it, and an edit on the host applies at the next start. See [Connecting an MCP server](../using/mcp.md).

| Key | Default | Meaning |
|---|---|---|
| `page` | the project's intake page | The address of the page a sign-in ends on. |
| `servers.<name>.url` | | The server's `https` address. |
| `servers.<name>.auth` | | `none`, `oauth` or `header`. |
| `servers.<name>.header` | | The header that carries the key, for `auth: header`. The key itself is in the secret store, never in the file. |
| `servers.<name>.timeout` | `30s` | How long one request may take. |
| `servers.<name>.tools` | | Each approved tool's rule: `auto`, `confirm` or `forbidden`. |
| `servers.<name>.fingerprint` | | Pins the approved tool list. |

```yaml
mcp:
  servers:
    shop:
      url: https://mcp.example.com/mcp
      auth: oauth
      tools:
        search_products: auto
```

## memory

| Key | Default | Meaning |
|---|---|---|
| `prompt_budget` | `24000` | The size in characters of the known-facts block of the agent's instructions. Over it, the topics changed longest ago fold to one line. |
| `review.enabled` | `true` | Read your quiet private chat once for facts the turn missed. |
| `review.quiet` | `10m` | How long the chat is silent before it is reviewed. |
| `archive_days` | `90` | How many days an ended conversation stays in the archive, where `/resume` and the agent's search of past chats find it. A conversation the agent has not reviewed yet stays until it is. Facts the agent remembers are kept. `off` keeps every conversation. |

## update

| Key | Default | Meaning |
|---|---|---|
| `check` | `true` | Ask once a day for a newer release and tell the admins once per version, with the version, its date and a link. It never installs one: you do, with `/update install`. Set `off` to stop the asking. Without a release version (a build from source) and with the database in memory, the node asks nothing. |

## bus

The node's message bus. By default the node runs a NATS server inside its own process and keeps its files in the `bus` folder beside the database; it opens no network port. Only a host edit changes this section: the agent cannot. See [The message bus](operations.md#the-message-bus).

| Key | Default | Meaning |
|---|---|---|
| `server` | `embedded` | `embedded` runs the server inside the node. `external` connects to a server, or a cluster, you run. |
| `urls` | none | The external server's addresses, `nats://` or `tls://` with a host and no user or password. Required for `external` and refused for `embedded`. |
| `credentials` | none | The text of a `.creds` file for the external server, as `env:NAME` or `store:NAME`. It is optional, only for `external`, and is kept in memory, never written to a file. |
| `trust` | none | `chain` puts the external server on the node's own trust chain: the server is configured with the lines `ganglion bus trust` prints, and the node connects with its own key. Only for `external`, and not together with `credentials`. The embedded server always runs on the chain. |

```yaml
bus:
  server: external
  urls: [nats://nats.example.com:4222]
  credentials: store:BUS_CREDENTIALS
```

## edge

For a node run as an edge, `ganglion run --roles edge`: a computer next to your devices that runs modules and links to your main node. The section counts only for that role. Only a host edit changes it: the agent cannot. See [Edges](operations.md#edges).

| Key | Default | Meaning |
|---|---|---|
| `name` | none | The edge's name in your network, one word with no dots or spaces, such as `garage`, and not `core`. Required for `--roles edge`. |
| `url` | none | Your main node's bus, `wss://<public address>/bus`. Left out, the edge runs on its own and keeps its events until it has a link. |
| `credentials` | `edge.creds` beside the database | The edge's credentials file, which your main node gives the edge when it joins. With `url` set, the edge refuses to start without it. |
| `telemetry_every` | `30s` | How often the edge sends its metrics and its state to your main node; `5s` or more. |

```yaml
edge:
  name: garage
  url: wss://example.com/bus
```

## backup

The daily encrypted backup to an S3-compatible bucket. Without this section there are no backups. Only a host edit changes it: the agent cannot. When the section is there, the first five keys are required. See [Backups](operations.md#backups).

| Key | Default | Meaning |
|---|---|---|
| `endpoint` | none | The bucket's S3 endpoint, an HTTPS address with a host and no path, such as `https://s3.example.com`. A plain `http` address is refused. |
| `region` | none | The signing region the provider names (`auto` at Cloudflare R2). |
| `bucket` | none | The bucket's name. |
| `access_key_id` | none | The id of the access key. Give the key the right to write into this bucket and no right to delete. |
| `secret_access_key` | none | The access key's secret, as `env:NAME` or `store:NAME`. The node does not start while it is not set. |
| `prefix` | empty | The folder in the bucket the copies go to. Empty puts them at the top. |
| `at` | `03:00` | The time of day, `HH:MM` in `node.tz`, the backup runs at. |

```yaml
backup:
  endpoint: https://s3.example.com
  region: us-east-1
  bucket: my-node-backups
  prefix: node
  access_key_id: EXAMPLEKEYID
  secret_access_key: env:BACKUP_SECRET_ACCESS_KEY
```

