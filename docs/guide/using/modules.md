# Reflex modules

A **reflex module** is a small program the agent writes for a routine task. It is compiled to WebAssembly and runs on a schedule, or when certain values change, without calling a model. It is fast and costs nothing per run.

A **cell** is a named value owned by a module, such as `battery.level`. A cell holds a sensor reading or a switch. A module writes only its own cells and reads another's only if it declared so. `/cells` shows all of them.

Modules need a kernel with Landlock (6.2+) and Rust on the node. See [Requirements](../getting-started/requirements.md).

## Ask for one

Describe the routine task to the bot in your private chat:

```text
Every 15 minutes read the price from https://example.com/api/price
and tell me when it falls below 100.
```

The agent chooses a template, copies it under a new name, edits the two files that matter, builds it in the background (a live card shows progress) and then offers to install it.

There are two templates:

- **reader**: reads an outside HTTP service on a schedule and reports the reading to a cell. It handles rate limits (waits and retries), marks a reading as stale when it gets too old, and stops retrying when a login fails.
- **reflex**: listens to cells and tells you when a rule fires, for example a value crossing a threshold downwards, or a value that has not changed for too long. It says each thing once.

A task often needs both: a reader feeds the cells, a reflex watches them.

## Approve

Before anything runs you get an install window with **Approve** and **Deny**. It lists the network hosts, secrets, cells, triggers, schedule and message limit. A new version that asks for more rights than the last says so on its own line. If the module ships examples, the window also shows what its trial run sent and wrote, so you see its messages and values before you approve. Read it.

Modules cannot be installed from a ready-made file, a link or a chat message. They are built from source on your node.

If a module needs a secret (for example an API key), set it on the host with `ganglion secret set --module <name> <NAME>`. Do not paste it in chat. See [Configure](../getting-started/configure.md#secrets).

## Versions and rollback

Every build is a new version, with a record of who built it and why. A build must carry a higher version than any earlier one. The node keeps the previous version. If a new version does not start, it is rolled back. Module state survives a swap.

## The `/modules` verbs

All of these work in your private chat and none restarts the node.

| Command | What it does |
|---|---|
| `/modules` | Lists modules. |
| `/modules <name>` | Shows a card: state, version, health, runs, last failure, settings, version history. |
| `/modules update <name>` | Updates a built-in module, one that ships inside the program, to the newest release. The node checks the release's checksum and permissions and runs a self-test first. If a check fails, the current version stays. It does not update modules the agent built: for those the bot answers that there is no such module. Ask the agent for a new version instead. |
| `/modules rollback <name>` | Goes back to the previous version. |
| `/modules off <name>` and `/modules on <name>` | Pauses and resumes a module. |
| `/modules remove <name>` | Removes a module. Its cells and history stay, read-only. |
| `/modules config <name> key=value` | Changes a setting the module declares. |

## The crash-loop rule

If a module fails 5 times within an hour, the node turns it off, raises an alert, and `/status` explains. Fix the cause (or ask the agent to), then turn it on again with `/modules on <name>`.

## Notification limits

A module may send you plain-text messages only if it was granted that, with a limit of up to 100 a day (counted in the node's time zone). Past the limit nothing is sent and you get one note. Each message is signed with the module's name and version, has no buttons, and is cut at 1000 characters. If you reply to one, the agent knows which module wrote it.

Other limits: one build at a time per node, and the agent builds a module at most 5 times in a row before you write something in that chat again.
