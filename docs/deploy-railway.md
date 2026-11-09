# Deploying on Railway

A runbook for running the node on Railway next to the current bot (R-LS-4,
task 4.7 of the [M4b plan](plans/p1-m4b-modules.md)). The image is built from
the [`Dockerfile`](../Dockerfile) in the repository root.

## The image

`docker build -t ganglion:m4 .` (about 1.2 GB, mostly the Rust toolchain and a
C linker that builds with a `build.rs` need).

| What | Where |
|---|---|
| The node | `/usr/local/bin/ganglion`, entrypoint `ganglion run --db /data/ganglion.db` |
| Rust (stable, target `wasm32-wasip1`) | `/opt/rust` (`RUSTUP_HOME=/opt/rust/rustup`, `CARGO_HOME=/opt/rust/cargo`), owned by root, readable by all, not writable by the node's user |
| The node's user | `ganglion`, uid 10001, home `/home/ganglion` |
| Builds | `/home/ganglion/.cache/ganglion/builds` (the builder's root is `os.UserCacheDir()/ganglion/builds`) |
| Examples | `/app/examples`, e.g. `/app/examples/modules/deye-reader` for `module.import` |
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

The image installs no model provider. Add one at deploy time, with the owner:

- **OpenRouter:** set `OPENROUTER_API_KEY` and name `key: env:OPENROUTER_API_KEY`
  in the `models:` section of `ganglion.yaml`.
- **The claude CLI:** it needs a login inside the container, which the image
  does not do. Install the CLI in a derived image and log in once over
  `railway ssh`; the login lives under the user's home, which is not on the
  volume, so it is lost at the next deploy. Prefer OpenRouter on Railway.

## The service

1. Create a second service in the same project as the current bot, from this
   repository and branch (Railway builds the `Dockerfile` by itself). Do not
   point it at the current bot's token: use the test bot's.
2. **Volume:** add a volume to the service, mount path `/data`.
3. **The volume and the user.** Railway mounts volumes owned by root, and its
   documentation suggests `RAILWAY_RUN_UID=0` for images that default to a
   non-root user. Do not set it: the node's command sandbox and the builder
   are off for root. The image starts as root only for
   [`docker-entrypoint.sh`](../docker-entrypoint.sh), which `chown`s `/data` to
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
5. **Memory:** give the service 1 GB while builds run (cargo and rustc peak
   there). The node's own limit is set in the file, `node.memory_limit:
   100MiB`: the node logs `node: memory limit` with `source=config` at start.
   Builds are separate processes outside that soft limit.
6. **Restart policy:** "on failure". The node exits with code 75 for
   `/restart`, and the platform starts it again.
7. **Region and size:** the same region as the current bot; nothing here needs
   more than the plan's CPU.

## `ganglion.yaml` in the volume

The file is `/data/ganglion.yaml`, beside the database (R-CFG-1). It must be a
file inside the volume directory, not a single-file bind mount: settings are
written by rename (and the install window by hard link), and both need a
directory. Create it over `railway ssh` or ship a first copy in the volume:

```yaml
node:
  memory_limit: 100MiB
telegram:
  token: env:TELEGRAM_BOT_TOKEN
  admin: telegram:user:<your id>
```

Add `models:` when the provider is chosen. [`ganglion.example.yaml`](../ganglion.example.yaml)
lists every key.

## What the kernel must offer

Module builds run in a Landlock sandbox that keeps writes to the build's own
folder. That needs **Landlock ABI 3 or later (Linux 6.2+)**; below that the node
refuses to build and says so. Railway containers share the host's kernel, so
check before relying on it:

1. `railway ssh`, then `uname -r`: 6.2 or later.
2. The node's log line `sandbox: ready` (or `sandbox: commands are off` with
   the reason), and the admin's `/status`, which shows the sandbox verdict.

Checked locally (2026-11-09, host kernel 6.18, Docker's default seccomp
profile, uid 10001): the node logs `sandbox: ready denied=60 hide_names=false`,
and `go test ./builder -run 'Rust|Real'` and `go test ./sandbox -run Confined`
pass in a container as that user with this image's Rust, so the default seccomp
profile does not block Landlock or the build helper. What Railway's runtime
allows cannot be known without deploying, so it is a deploy-time check: in the
node's log after the first start, the line must read `sandbox: ready ...`; if it
reads `sandbox: commands are off`, its `reason` names what is missing (kernel,
root, or dumpable) and nothing can be built.

If the sandbox is off, the node still runs the echo bot, the built-in modules
and installed modules; it only builds nothing.

## Checks after the first start

Over `railway ssh`:

```sh
setpriv --reuid=10001 --regid=10001 --clear-groups cargo --version   # as the ganglion user
ls -ld /data /home/ganglion          # both writable by the user
uname -r
ganglion mcp --db /data/ganglion.db  # read-only diagnostics over stdio
```

`ganglion mcp` is how you read logs, turns and settings: point a local MCP
client at `railway ssh -- ganglion mcp --db /data/ganglion.db` (see
[diagnostics](README.md#diagnosing-the-node-from-outside)).

Then, in the bot: `module.import` the Deye example from
`/app/examples/modules/deye-reader`, build it, press the install window, set
`inverter_sn` under `modules:` and check `/modules` and `/cells`.

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

## Not covered here

The Railway deploy itself is the owner's step (task 11 of the plan); this page
and the image are the preparation. A custom domain and a webhook are not
needed: the bot polls Telegram.
