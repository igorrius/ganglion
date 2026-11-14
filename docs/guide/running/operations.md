# Operations

## Is it healthy

Send `/status` in your private chat. It starts with a verdict (all well, needs a look, not working) and then lists the node, the models and today's usage, Telegram delivery, the sandbox, queues, scheduled jobs, modules and problems. Look here first.

## Logs

The node writes readable lines to standard output (`docker logs` shows them) and JSON lines to files in `logs/` beside the database. Change the place with `log.dir` and the detail with `log.level`.

Files rotate at 10 MiB. The node keeps 5 old files for up to 14 days. Secrets and tokens are redacted from both.

Useful start-up lines: `sandbox: ready` (host commands and builds work), `sandbox: commands are off` with a reason (they do not), `node: memory limit`, `telegram: webhook registered`.

## Metrics

A Prometheus endpoint listens on `127.0.0.1:9464/metrics` (`metrics.addr`; `off` runs none). A small dashboard is at `/webmetrics/` on the same address. In Telegram, `/metrics` sends the numbers as a picture. The metrics are never on the public listener.

## Diagnostics with `ganglion mcp`

`ganglion mcp` is a read-only diagnostics server for an MCP client such as Claude Code. It reads the database and logs without a running node or a model. Its tools search logs and the tool-call journal, list failed turns, show a turn, show the configuration and show the queues.

```sh
ganglion mcp --db /path/to/ganglion.db
```

A client configuration:

```json
{"mcpServers": {"ganglion": {"command": "ganglion", "args": ["mcp", "--db", "/path/to/ganglion.db"]}}}
```

For a node on another machine, run it over SSH (`ssh host ganglion mcp --db /data/ganglion.db`), or in a container with `docker exec -i <container> ganglion mcp --db /data/ganglion.db`. Use `--log-dir` if the logs are not beside the database.

Whoever can run it can read all conversation history, and the logs quote web pages. Do not connect it to a client session that approves commands automatically.

## Backups

**ganglion has no built-in backup.** You copy the files yourself. Back up the whole data directory, which means:

- `ganglion.db` (the database),
- `master.key` (or the value of `GANGLION_MASTER_KEY`, saved separately),
- `ganglion.yaml`,
- `workspace/` (persona, memory, module sources).

The installed module files in `modules/` are described in the database, but copy the folder too. `modules/templates` is rewritten at each start and does not matter.

Keep the key with the database. A database without its key yields no secrets.

A safe way is to stop the node, copy the folder, and start it again. If you cannot stop it, take a consistent copy of the database with SQLite instead of copying the file, then copy the rest:

```sh
sqlite3 /data/ganglion.db ".backup '/backup/ganglion.db'"
```

Copying a live `ganglion.db` together with its `-wal` and `-shm` files is not safe. To restore, put the files back as the node's user and start the node.

## Updating

There is no built-in updater yet. Take a [backup](#backups) first, because formats can change between 0.x versions, and read the release notes.

With the image, pull the new one and start a new container from it with the same volume and settings:

```sh
docker pull ghcr.io/igorrius/ganglion:latest
docker stop ganglion && docker rm ganglion
# then the same docker run as in Install
```

From source, get the new code, rebuild and restart:

```sh
git pull
make build
```

Module templates and built-in skills update with the program.

## Restarting

`/restart` in your private chat stops the node cleanly with exit code 75. It only comes back if something starts it again, so run it under a supervisor with a restart-on-failure policy: Docker (`--restart on-failure`), systemd (`Restart=on-failure`) or your platform's setting. The image has no supervisor of its own: its entrypoint only starts the node. Without a supervisor the node just stops. Changes to any setting outside `modules` need a restart.

Memory: give a container about 1 GB if you want module builds. Set `node.memory_limit` lower than that for the node itself.
