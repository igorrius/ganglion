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

A node moves to a new version only when you say so, and only to a release the project has signed. Nothing updates by itself. Take a [backup](#backups) first, because formats can change between 0.x versions, and read the release page before you install.

**From the chat.** In your private chat (an admin only):

- `/update` shows the version the node runs and the newest release, with its date and a link to its page. When the newest is newer, it tells you the command to send.
- `/update install vX.Y.Z` downloads that release, checks the project's signature and the file's checksum, keeps a copy of the database and restarts the node into the new version. Writing the version is the confirmation. You can name a pre-release too, if it is newer than the version you run.
- `/update rollback` goes back to the version you ran before. Send it again to go forward.

A release is refused, and the node keeps running as it was, when it is not signed by the project, does not match its checksum, is not newer than the running version, has no build for your machine, or needs a newer image than yours. The reply says which. A refusal never changes anything.

**From the host.** `ganglion update` does the same on the machine, as the node's user, with `--db` pointing at the node's database: `ganglion update` shows the versions, `ganglion update install vX.Y.Z` installs and `ganglion update rollback` goes back. It does not stop a running node; the new version starts at the node's next restart (`/restart`, or restart the container).

**What happens at the restart.** The new version gets two tries to start. When it has run for two minutes, the node confirms it and tells the admins in the chat. When it fails to start twice, the node goes back to the version it ran before, puts back the database as it was before the update if the new version had changed it, and tells the admins what happened. The update restarts the node with exit code 75, so the node must run under a supervisor that restarts it (see [Restarting](#restarting)).

The program that came with the image is never replaced. Releases are kept in the `releases/` folder beside the database, which host commands and modules cannot read or write; the node keeps the running version and the one before it.

**A build without a release version.** A node built from source, or any build whose version is not exactly a release (`dev`, or a version with a commit suffix), cannot be moved with `/update`. Move it once to the image `ghcr.io/igorrius/ganglion:<version>` (the version of the newest release, without the `v`), with the same volume and settings. After that `/update` works. `/update` says this and names the image.

**What `/update` does not change.** It replaces the node's program only. The Rust toolchain used for module builds, the `claude` command line tool and the rest of the image are updated by pulling a newer image:

```sh
docker pull ghcr.io/igorrius/ganglion:<version>
docker stop ganglion && docker rm ganglion
# then the same docker run as in Install, with the new tag
```

A release that needs a newer image than yours is refused with a message that says so; pull the new image first. If the node does not know its image (it is not run from one), `/update` says that it cannot check this.

From source, get the new code, rebuild and restart:

```sh
git pull
make build
```

Module templates and built-in skills update with the program.

## Restarting

`/restart` in your private chat stops the node cleanly with exit code 75. It only comes back if something starts it again, so run it under a supervisor with a restart-on-failure policy: Docker (`--restart on-failure`), systemd (`Restart=on-failure`) or your platform's setting. The image has no supervisor of its own: its entrypoint only starts the node. Without a supervisor the node just stops. Changes to any setting outside `modules` need a restart.

Memory: give a container about 1 GB if you want module builds. Set `node.memory_limit` lower than that for the node itself.
