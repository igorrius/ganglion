# Deploying on Railway

A runbook for running the node on Railway next to the current bot (R-LS-4,
task 4.7 of the [M4b plan](plans/p1-m4b-modules.md)). The image is built from
the [`Dockerfile`](https://github.com/igorrius/ganglion/blob/master/Dockerfile) in the repository root.

## The image

`docker build -t ganglion:m4 .` (about 1.2 GB, mostly the Rust toolchain and a
C linker that builds with a `build.rs` need).

| What | Where |
|---|---|
| The node | `/usr/local/bin/ganglion`, entrypoint `ganglion run --db /data/ganglion.db` |
| Rust (stable, target `wasm32-wasip1`) | `/opt/rust` (`RUSTUP_HOME=/opt/rust/rustup`, `CARGO_HOME=/opt/rust/cargo`), owned by root, readable by all, not writable by the node's user |
| The node's user | `ganglion`, uid 10001, home `/home/ganglion` |
| Builds | `/home/ganglion/.cache/ganglion/builds` (the builder's root is `os.UserCacheDir()/ganglion/builds`) |
| Module templates | In the binary; each start writes them to `/data/modules/templates` for `module.import` |
| Data | `/data` (a volume): `ganglion.db`, `ganglion.yaml`, `logs/`, `modules/`, the workspace |

Three details matter to the builder:

- The user needs a real `HOME` so the cache folder exists. There is no
  `~/.cargo/config.toml` for it, and there must not be one: the builder
  refuses every build when it finds one.
- Rustup's home is read-only for the node. `RUSTUP_AUTO_INSTALL=0` is set, so
  rustup never tries to install a toolchain at run time. The node asks rustc
  for its toolchain at start and pins every build to it.
- The sandbox refuses to run commands as root, so the node must run as
  `ganglion`: the entrypoint drops to it (see "The service", step 3). Over
  `railway ssh` or `docker exec` you are root; use
  `setpriv --reuid=10001 --regid=10001 --clear-groups <command>` to act as the
  node's user.

The image carries the claude CLI as one root-owned binary,
`/usr/local/bin/claude` (`--build-arg CLAUDE_CLI=off` leaves it out). Choose
the provider at deploy time, with the owner:

- **The claude CLI:** set `CLAUDE_CODE_OAUTH_TOKEN` on the service (from
  `claude setup-token`) and list the `claude-cli` provider in `models:`. No
  login inside the container is needed: the CLI reads the token from the
  environment, which the provider passes to it, and keeps its own files in
  `/home/ganglion/.claude`, outside the volume. Commands the agent runs do not
  get the token (a clean environment), but a module could be granted it by its
  manifest until board card G-132 is closed: read the install window.
- **OpenRouter:** set `OPENROUTER_API_KEY` and name `key: env:OPENROUTER_API_KEY`
  in the `models:` section of `ganglion.yaml`.

## The service

1. Create a second service in the same project as the current bot, from this
   repository and branch (Railway builds the `Dockerfile` by itself), or with
   the CLI: `railway add -s <service>`, then `railway up -s <service>` from the
   repository. Always name the service: `railway link` selects the current
   bot's service by itself. Do not point it at the current bot's token: use
   the test bot's.
2. **Volume:** add a volume to the service, mount path `/data`
   (`railway volume --service <service id> add -m /data`; with a service's
   name CLI 5.71 panics). The `Dockerfile` has no `VOLUME` line, because
   Railway refuses a Dockerfile that has one.
3. **The volume and the user.** Railway mounts volumes owned by root, and its
   documentation suggests `RAILWAY_RUN_UID=0` for images that default to a
   non-root user. Do not set it: the node's command sandbox and the builder
   are off for root. The image starts as root only for
   [`docker-entrypoint.sh`](https://github.com/igorrius/ganglion/blob/master/docker-entrypoint.sh), which `chown`s `/data` to
   uid 10001 when it is not already owned by it and then drops to that user
   (`setpriv`) before the node starts. Tried locally with a root-owned Docker
   volume; confirm on Railway with `ls -ld /data` (owner `ganglion`) and that
   the node's process runs as uid 10001 (`cat /proc/<pid>/status`, `Uid:`).
4. **Variables:**

   | Variable | Value |
   |---|---|
   | `TELEGRAM_BOT_TOKEN` | the test bot's token (never the live bot's) |
   | `DEYE_APP_ID`, `DEYE_APP_SECRET`, `DEYE_EMAIL`, `DEYE_PASSWORD` | the DeyeCloud credentials the module lists in its manifest |
   | `OPENROUTER_API_KEY` | a provider key, if the agent runs through OpenRouter |

   Secrets are written in `ganglion.yaml` as `env:NAME` and never in the file.
   A module's secrets need no variable: see [Module secrets](#module-secrets).
   A variable can refer to the current bot's (`${{<other-service>.DEYE_APP_ID}}`),
   but not to a sealed one: a sealed variable resolves to an empty value, so
   set those by value.
5. **Memory:** give the service 1 GB while builds run (cargo and rustc peak
   there). The node's own limit is set in the file, `node.memory_limit:
   100MiB`: the node logs `node: memory limit` with `source=config` at start.
   Builds are separate processes outside that soft limit. Measured on
   Railway (2026-11-11, `deye-reader`): the container peaked at 1.74 GB
   during the build, file cache included, and 0.92 GB stayed afterwards, of
   which 874 MB was the kernel's cache of the Rust toolchain's files and
   36 MB anonymous memory. Railway counts that cache as used memory and the
   container cannot ask its cgroup to reclaim it (`/sys/fs/cgroup` is
   read-only), so the builder advises the kernel to drop the toolchain's
   cached pages when a build ends.
6. **Restart policy:** "on failure". The node exits with code 75 for
   `/restart`, and the platform starts it again.
7. **Region and size:** the same region as the current bot; nothing here needs
   more than the plan's CPU.

`railway ssh` and `railway volume files` need an SSH key registered in the
Railway account (`railway ssh keys add`); the CLI's login is not enough.

## Updating the node from the chat

A service built from the repository (`railway up`) reports a build without a
release version, which `/update` refuses (it says so and names the image).
Move the service to the published image once, then update from the chat:

1. In the service's settings change the source to the image
   `ghcr.io/igorrius/ganglion:<version>` (the version of a release, without the
   `v`), keeping the volume at `/data`, the variables and the restart policy.
   Redeploy. The data, the database and `ganglion.yaml` stay on the volume.
2. After the start, `/update` in the admin's private chat shows the running
   version and the newest release.
3. `/update install vX.Y.Z` installs the next release and the node restarts
   into it; `/update rollback` goes back. The release files live in
   `/data/releases`, on the volume, so a redeploy of the same image keeps the
   active release: the entrypoint starts the image's own binary, and the launcher
   at the start of `ganglion run` moves on to the installed one. The image's
   binary is the floor the node falls back to.

Pull a newer image (change the tag) only when a release needs one (`/update`
says so) or to update the Rust toolchain and the `claude` CLI; `/update`
replaces the node's program and nothing else. Take a copy of the volume first
when a release changes the database. Check after an install: `/status` shows
the version, and the admins are told when the new version is confirmed two
minutes after its start, or that the node went back.

## `ganglion.yaml` in the volume

The file is `/data/ganglion.yaml`, beside the database (R-CFG-1). It must be a
file inside the volume directory, not a single-file bind mount: settings are
written by rename (and the install window by hard link), and both need a
directory. Create it over `railway ssh` as the node's user, so the node can
rewrite it (a file root creates is root's):

```sh
setpriv --reuid=10001 --regid=10001 --clear-groups sh -c 'cat > /data/ganglion.yaml'   # paste, then Ctrl-D
```

or ship a first copy in the volume (the entrypoint hands every file in
`/data` to uid 10001 only when `/data` itself is not that user's, so `chown
10001:10001 /data/ganglion.yaml` after copying it in as root). The first
copy:

```yaml
node:
  memory_limit: 100MiB
telegram:
  token: env:TELEGRAM_BOT_TOKEN
  admin: telegram:user:<your id>
```

Add `models:` when the provider is chosen. [`ganglion.example.yaml`](https://github.com/igorrius/ganglion/blob/master/ganglion.example.yaml)
lists every key.

## Module secrets

A module's secret is stored by the node, encrypted, in its database
([ADR-0077](adr/0077-encrypted-secret-store-on-the-nodes-key.md)); a new
secret is no new variable and no redeploy. Set one over `railway ssh`, as
the node's user, with the value on standard input:

```sh
setpriv --reuid=10001 --regid=10001 --clear-groups \
  sh -c 'printf %s "$DEYE_APP_ID" | ganglion secret set --db /data/ganglion.db --module deye-reader DEYE_APP_ID'
```

- The module reads the value at its next run; nothing restarts. `/secrets`
  in the admin's private chat lists the names (never a value), and
  `ganglion secret list` does the same on the host.
- **Moving a secret from a variable:** the command above reads the variable
  the service still has. When `/secrets` shows the name and the module's
  next run is healthy, delete the variable in Railway
  (`railway variable delete NAME -s <service>`) and then redeploy
  (`railway redeploy -s <service> --yes`): deleting a variable does not
  restart the service, and the running container keeps the old value until
  it does. A module reads its own stored secret first and the environment
  second, so both may exist during the move.
- **The key** is `/data/master.key`, made by the node at its first start
  (mode `0600`). It lies on the volume with the database: a backup or a
  copy of the volume must hold both, and the database without the key
  yields no secret. To keep the key off the volume, set
  `GANGLION_MASTER_KEY` (base64 of 32 bytes: `openssl rand -base64 32`) as
  a sealed variable before the first secret is stored.
- **A key that does not fit** (a restored database with another key, a
  changed variable) does not stop the node: the store is locked, every
  stored secret reads as not set, `/status` says so. Give the node the old
  key back, or send `/secrets reset` to drop the stored secrets and set
  them again.
- Run the command as uid 10001: a `master.key` that root creates is not
  readable by the node.
- One of the node's own secrets can be stored the same way, without
  `--module`, and named in the file as `store:NAME` instead of `env:NAME`.
  The node reads those at its start, so changing one needs `/restart`.

## Telegram by webhook

The owner's node runs this way since 2026-11-13. Polling needs none of
this and stays the default. The address of a running node is never written
in this repository: anyone can send requests to a public address, and the
platform bills the traffic. For the same reason prefer a domain that cannot
be guessed from the service's name (Railway lets a generated domain be
edited) over the default `<service>-<environment>` one. To have Telegram call the
node instead (`telegram.updates: webhook`, R-DLV-18,
[ADR-0078](adr/0078-telegram-updates-by-polling-or-webhook.md)):

1. Generate a domain for the service (Settings → Networking, or `railway
   domain -s <service>`). It has the form
   `<service>-<environment>.up.railway.app`; no target port has to be chosen
   when the node listens on the port Railway gives (`PORT`, `8080` on
   2026-11-13).
2. Add to `/data/ganglion.yaml`:

   ```yaml
   http:
     addr: ":8080"
     public_url: https://<the domain>
   telegram:
     updates: webhook
   ```

3. Restart the node (`/restart` in the chat, or a redeploy). At start it
   asks its own domain for a value made for this start, takes the updates
   that waited by polling, then registers the webhook. The log says
   `telegram: webhook registered`, and `/status` shows `Telegram: webhook`.
4. If the domain does not reach the node, or Telegram's deliveries keep
   failing, the node tells the admins in the chat and polls until the next
   restart, so a wrong address never cuts the chat off (R-DLV-19).

Deploying from a git worktree: `railway up` uploads the main checkout, not
the worktree. Name the folder: `railway up <path> --path-as-root -s <service>`.

The way back: set `telegram.updates: polling` (the `http` section may stay
or go) and restart. The node removes the webhook it set and polls. The
domain can then be deleted.

The listener answers the webhook's path and `/healthz` and nothing else;
the metrics stay on `127.0.0.1:9464` and are never on the domain. The
measurements behind these steps are in the
[research note](research/2026-11-13-telegram-webhook-on-railway.md).

## What the kernel must offer

Module builds run in a Landlock sandbox that keeps writes to the build's own
folder. That needs **Landlock ABI 3 or later (Linux 6.2+)**; below that the node
refuses to build and says so. Railway containers share the host's kernel, so
check before relying on it:

1. `railway ssh`, then `uname -r`: 6.2 or later.
2. The node's log line `sandbox: ready` (or `sandbox: commands are off` with
   the reason), and the admin's `/status`, which shows the sandbox verdict.

Checked locally (2026-11-09; the start and the layout test again on 2026-11-10;
host kernel 6.18, Docker's default seccomp profile, uid 10001): the node logs
`sandbox: ready denied=62 hide_names=false` (the node runs in `/data`, so the
sandbox denies `ganglion.yaml`, the database, its journal files and its lock
there one by one), and `go test ./builder -run 'Rust|Real'`, `go test ./sandbox
-run Confined` and the sandbox's test of this layout
(`TestTheImagesDataFolderKeepsTheSettingsAndTheLock`) pass in a container as
that user with this image's Rust, so the default seccomp profile does not block
Landlock or the build helper. What Railway's runtime allows cannot be known
without deploying, so it is a deploy-time check: in the node's log after the
first start, the line must read `sandbox: ready ...`; if it reads `sandbox:
commands are off`, its `reason` names what is missing (kernel, root, or
dumpable) and nothing can be built.

Checked on Railway (2026-11-11, on the owner's service):
kernel `6.18.15+deb13-cloud-amd64`; the volume is ext4, handed to `ganglion` by
the entrypoint, and hard links work on it; the node runs as uid 10001 and logs
`sandbox: ready denied=62 hide_names=false`, `node: memory limit
bytes=104857600 source=config` (the container's own limit is 8 GB); idle RSS
47 MB with the agent on; `claude -p` answers as that user with the token.
Still open: a module build there and its peak memory, and the comparison below.

If the sandbox is off, the node still runs the echo bot, the built-in modules
and installed modules; it only builds nothing.

## Checks after the first start

Over `railway ssh`:

Every command that touches `/data` runs as the node's user: one run as root
would leave root's files there (the database's `-wal` and `-shm`, a log),
which the node cannot open after its next start.

```sh
setpriv --reuid=10001 --regid=10001 --clear-groups cargo --version   # as the ganglion user
ls -ld /data /home/ganglion          # both writable by the user
uname -r
setpriv --reuid=10001 --regid=10001 --clear-groups ganglion mcp --db /data/ganglion.db  # read-only diagnostics over stdio
```

`ganglion mcp` is how you read logs, turns and settings: point a local MCP
client at `railway ssh -- setpriv --reuid=10001 --regid=10001 --clear-groups
ganglion mcp --db /data/ganglion.db` (see
[diagnostics](README.md#diagnosing-the-node-from-outside)).

Then, in the bot: ask for a module. The agent imports one of the templates
the binary ships, writes the module, builds it and offers the install
window; set the module's settings under `modules:` and check `/modules` and
`/cells`.

## Comparing with `deye.py` (R-LS-4)

For a few days both readers write the battery's state of charge every 30
minutes: `deye.py` in the current bot, `deye-reader.soc` in this node. Compare
by day:

- Export `deye-reader.soc` history with `/cells` (or `cells.list` and the
  history it condenses) and the matching rows of the current bot's store.
- For each day, count the pairs that differ by more than 1 point and list the
  gaps (a half-hour with no reading on either side).
- A match is: the same points of the day within 1 percentage point, and no gap
  on the module's side that `deye.py` does not share. A 429 from DeyeCloud on
  both sides at once is not a difference.

When a few days agree, the owner decides to migrate the bot (spec R-LS-4).

On 2026-11-11 the owner compared the messages of the two bots, the current
one and this node's `battery-alerts` over `deye-reader`, found them identical
and closed the comparison after one day. The by-day export above was not run.

## Not covered here

While the test bot's token runs on Railway, a local `make live-agent` on the
same bot gets `409 Conflict` and one of the two stops polling: stop the
service (or use another bot) for local live checks. A custom domain is not
needed, and a generated one only for the webhook mode above: by default the
bot polls Telegram.
