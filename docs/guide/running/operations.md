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

## Installing a skill

A skill is a procedure the agent reads before it follows it. Only you install one; the agent may propose the text.

1. Make `workspace/skills/<name>/SKILL.md`: a frontmatter with `name` (the directory's), `description`, optionally `model` and `script`, then the steps in Markdown.
2. A script sits beside it, executable, named by `script.run`, with `effects: read` (runs by itself) or `effects: change` (asks with a button). In a session that has read outside content, such as a web page, a read script given input asks too, since it may reach the network. It gets only `PATH`, `HOME`, `LANG`, `TZ` and the names in `script.env`; the node's secrets never.
3. Restart the node; the log names a skill that was not loaded and why.

A script runs in the same sandbox as host commands. If the sandbox is off, the script is not offered and `/status` says why.

## Correcting the bot's texts

What the bot writes without a model follows each person's language: the one they asked for in the chat (for now only an admin can ask), then their Telegram's, then English. English, Russian and Ukrainian ship in the binary; any other language is translated once by the cheap model, and each person is told so once.

To correct a text, put `<code>.yaml` in the `languages/` directory beside the database (`agent.languages` moves it, but never into the workspace), with the keys you want from [`texts/lang/en.yaml`](https://github.com/igorrius/ganglion/blob/master/texts/lang/en.yaml), and restart. A text keeps its `{placeholders}` and adds no links, mentions or Markdown; one that does not is logged and left out. Your file comes before a shipped file and a translation, key by key.

## Backups

A node can back itself up every day to a bucket at any S3-compatible provider. Each copy is encrypted on the node before it leaves, so the provider sees only sealed bytes. Without a `backup:` section there are no backups and `/status` says so; the [manual copy](#backing-up-by-hand) below then is your only protection.

**What a backup holds:** a consistent copy of the database, the whole `workspace/` (persona, memory, module sources, with its git history), `modules/` without `modules/templates`, and `ganglion.yaml`. It never holds `master.key`, `releases/`, `logs/` or the `claude` work folder.

### Turning backups on

1. **Keep the master key off the node first.** A backup opens only with the node's master key (`master.key` in the data directory, or the value of `GANGLION_MASTER_KEY`). Without the key a copy is useless, and a key that lives only on the node is lost with it. Save it in a password manager now: for a key file, `base64 < /data/master.key`; for the variable, its value.
2. **Make a bucket** at a provider, and a key for it:
   - *Backblaze B2:* make a private bucket, then an application key limited to that bucket, with read and write rights but **without `deleteFiles`**. Note its key ID and the key itself, and the bucket's S3 endpoint and region from the bucket's page.
   - *Cloudflare R2:* make a bucket and add a bucket lock rule, so objects cannot be deleted or overwritten for a number of days. Make an API token limited to that bucket, with object write rights, and take its S3 endpoint; the region is `auto`.
3. **Decide how long copies are kept.** The node never lists or deletes anything in the bucket, so the bucket does it: set a lifecycle rule that removes copies after, say, 30 days.
4. **Give the node the secret key.** Put it in an environment variable (`env:NAME`), or store it with `ganglion secret set BACKUP_SECRET_ACCESS_KEY` and refer to it as `store:BACKUP_SECRET_ACCESS_KEY`. It is never written in the file.
5. **Add the section to `ganglion.yaml`** and restart:

```yaml
backup:
  endpoint: https://s3.example.com
  region: us-east-1
  bucket: my-node-backups
  prefix: node
  access_key_id: EXAMPLEKEYID
  secret_access_key: env:BACKUP_SECRET_ACCESS_KEY
  at: "03:00"
```

The node refuses to start if the secret key is not set. See the [reference](configuration.md#backup) for each key. Only you can change this section, on the host: the agent cannot change it or start a backup.

The node makes one backup a day at `at`, in its own time zone (`node.tz`). It makes none at start. To make one now, for a first check or before an update, run as the node's user:

```sh
ganglion backup now
```

It prints the object's name and size, or says why it failed and exits with an error. Check that the object appears in the bucket.

### What you see

- `/status` shows a line for backups. It says **off** with no section, the time and size of the last copy when all is well, a **warning** when there has been no good copy for 36 hours (a node that has never made one is given 36 hours after its start), and a **failure** with the reason when the last run failed.
- When a backup fails, the admins get one message in their private chat with the reason in plain words (the bucket refused the upload and with what status, could not be reached, or an error the log describes). It is sent once, however many days the failure lasts; when a backup works again, one more message tells you the size and the time.
- The node's metrics count backups by outcome and show the time and size of the last good one.

### Restoring

Restore on any machine that has the `ganglion` binary, into a directory that does not exist or is empty. It never writes over a node's data.

1. **Get the archive.** Download the `.tar.gz.enc` object you want with the provider's own tool or web page; the node's key cannot read the bucket.
2. **Have the master key** from your password manager, as a file of the 32 raw bytes (what `master.key` is) or as the variable `GANGLION_MASTER_KEY` (base64).
3. **Restore:**

```sh
ganglion backup restore --data /restore/node --key-file /safe/master.key ganglion-20260101T030000Z.tar.gz.enc
```

   Without `--key-file` the key is read from `GANGLION_MASTER_KEY` or `GANGLION_MASTER_KEY_FILE`. The command makes no key. It opens the whole archive and checks every file and the database before it writes anything, and it leaves nothing behind when it fails. A wrong key, a damaged archive or a directory that is not empty is refused.
4. **Put the key back.** Copy the master key into the restored directory as `master.key`, or set `GANGLION_MASTER_KEY`, then start the node on it: `ganglion run --db /restore/node/ganglion.db`. The restored `ganglion.yaml` is the one the old node had; check the paths and secrets it names (a secret in an environment variable must be set on the new machine).

Restore once before you need it, on a spare machine, so the first time is not during a loss.

### Backing up by hand

Without a bucket you copy the files yourself. Back up the whole data directory, which means:

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

A node moves to a new version only when you say so, and only to a release the project has signed. Nothing updates by itself. Once a day the node asks for the newest release and, when there is a newer one, tells the admins once in their private chat: the version, its date and a link to its page, with an Install button. To stop the asking, set `update.check: off` in `ganglion.yaml`. Take a [backup](#backups) first, because formats can change between 0.x versions, and read the release page before you install.

**From the chat.** In your private chat (an admin only):

- `/update` shows the version the node runs and the newest release, with its date and a link to its page. When the newest is newer, it tells you the command to send and has an Install button for it; when there is a version to go back to, it has a Roll back button too.
- `/update install vX.Y.Z` downloads that release, checks the project's signature and the file's checksum, keeps a copy of the database and restarts the node into the new version. Writing the version is the confirmation. You can name a pre-release too, if it is newer than the version you run.
- `/update rollback` goes back to the version you ran before. Send it again to go forward.

A release is refused, and the node keeps running as it was, when it is not signed by the project, does not match its checksum, is not newer than the running version, has no build for your machine, or needs a newer image than yours. The reply says which. A refusal never changes anything.

**Buttons.** The Install button of `/update` and of the daily notice does what `/update install` does for the version it names, and the Roll back button does what `/update rollback` does. Pressing one is the confirmation, and only an admin can press it. The buttons of `/update` work for a day and the one in the daily notice for a week. After a press the message says what happened and the buttons go. A button for a version that is already installed changes nothing, and a Roll back button that no longer matches the version you would go back to says it is out of date; send `/update` for fresh ones.

**From the host.** `ganglion update` does the same on the machine, as the node's user, with `--db` pointing at the node's database: `ganglion update` shows the versions, `ganglion update install vX.Y.Z` installs and `ganglion update rollback` goes back. It does not stop a running node; the new version starts at the node's next restart (`/restart`, or restart the container).

**What happens at the restart.** The new version gets two tries to start. As soon as it starts, the node tells the admins in the chat; when it has kept running for two minutes, the node confirms it there. Two minutes, because a version can start and still fail shortly after; only one that keeps running is kept. After `/update rollback` the node says which version it runs once it is back. When it fails to start twice, the node goes back to the version it ran before, puts back the database as it was before the update if the new version had changed it, and tells the admins what happened. The update restarts the node with exit code 75, so the node must run under a supervisor that restarts it (see [Restarting](#restarting)).

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
