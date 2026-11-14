# Troubleshooting

Each entry reads: what you see, why, and what to do. When in doubt, send `/status` first.

## The bot never replies to me

**Cause:** `telegram.admin` is empty or wrong. Nobody can use the bot, and strangers get no reply.
**Fix:** Set it to `telegram:user:` followed by your numeric Telegram user id, then restart.

## The bot only repeats my message

**Cause:** The configuration has no `models` section, so the agent is off.
**Fix:** Add `models` with at least one provider, one model and a `chat` purpose. See [Models](running/models.md).

## The node stops at start with an error about a key

**Cause:** The configuration is checked strictly. An unknown key, a literal secret in a field that takes `env:` or `store:`, or a retired setting stops the start. The message names the key.
**Fix:** Correct the named key. Compare with `ganglion.example.yaml`.

## Telegram 409 Conflict, or the bot stops receiving

**Cause:** Two processes poll the same bot token, often a test node and the real one.
**Fix:** Stop the extra one, or use a separate bot for testing. See [Telegram updates](running/telegram-updates.md#one-poller-per-token).

## A group does not answer

**Cause:** Either the group was not allowed, or the message did not address the bot.
**Fix:** Look for the Allow/Refuse card in your private chat and press Allow. Then mention the bot, reply to one of its messages, or use `/command@your_bot`. A bare `/status` in a group gets no answer, and admin-only commands never work in a group.

## A command says "This command is not available to you."

**Cause:** Admin commands work only in the admin's private chat.
**Fix:** Send it there. See [Commands](using/commands.md).

## "Daily limit reached"

**Cause:** You hit the daily turns, tokens or budget limit. Days follow `node.tz`, which defaults to `Europe/Kyiv`.
**Fix:** Wait for the next day, or raise the limits. See [Models](running/models.md#limits).

## Reminders and "daily" things happen at the wrong hour

**Cause:** The time zone default is `Europe/Kyiv`.
**Fix:** Set `node.tz` to your IANA zone, for example `America/New_York`, and restart.

## My settings do not apply

**Cause:** Only the module list changes live. Everything else, including values from `store:`, is read at start.
**Fix:** Send `/restart`, or restart the process or container.

## `/restart` just stops the node

**Cause:** Nothing starts it again.
**Fix:** Run under a supervisor with a restart-on-failure policy. See [Operations](running/operations.md#restarting).

## Settings are lost, or the node cannot save its file

**Cause:** `ganglion.yaml` is a single-file mount, or it is owned by root. The node rewrites the file (groups, modules, destinations), which needs a directory and ownership by the node's user.
**Fix:** Keep the file inside the data directory, owned by the node's user (uid 10001 in the image).

## The node fails after I ran a command in the container

**Cause:** You ran a command that touches `/data` as root. Root's database `-wal` and `-shm` files, or a root-made `master.key`, lock the node out.
**Fix:** Stop the node, give the files back (`chown -R 10001:10001 /data`), and always run such commands as uid 10001.

## Host commands or module builds are off

**Cause:** The log says `sandbox: commands are off`. The reason is one of: not Linux, running as root, the kernel has no Landlock, or a self-test failed. Module builds also need Landlock ABI 3 (Linux 6.2) and are refused below it.
**Fix:** Run as an ordinary user on a kernel 6.2 or later. Check with `uname -r`. See [Requirements](getting-started/requirements.md).

## Docker or `systemctl --user` fails inside a command

**Cause:** A sandboxed command can open no Unix socket. Landlock reaches socket files only from ABI 9, and the node does not use that yet, so it refuses Unix sockets to every command. Docker and `systemctl --user` reach their services over one.
**Fix:** Run those commands yourself, outside the bot.

## A module will not build

**Cause:** Rust or the `wasm32-wasip1` target is missing, a `~/.cargo/config.toml` exists, there is no C linker, or memory is under about 1 GB.
**Fix:** Install the target (`rustup target add wasm32-wasip1`), remove the config file, install `build-essential`, and give the machine more memory.

## A module turned itself off

**Cause:** It failed 5 times within an hour.
**Fix:** Read its card with `/modules <name>`, fix the cause, then `/modules on <name>`.

## Secrets read as unset

**Cause:** The master key does not match the database, for example after a restore.
**Fix:** Put the old `master.key` (or `GANGLION_MASTER_KEY`) back. Or send `/secrets reset` and enter the secrets again. See [Configure](getting-started/configure.md#the-master-key).

## The agent asks for a button press for everything

**Cause:** The session is tainted: it read outside content.
**Fix:** Send `/new`. See [Tools and safety](using/tools-and-safety.md#tainted-sessions).

## The webhook does not work

**Cause:** The public address does not reach the node. The node falls back to polling and warns in `/status`.
**Fix:** Fix the address or the proxy, or set `telegram.updates: polling`. See [Telegram updates](running/telegram-updates.md).
