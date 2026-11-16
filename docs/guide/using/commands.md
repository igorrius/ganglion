# Commands

Send a command as a message. The `/command@your_bot` form works too and is required in groups. Command words are not case sensitive. A forwarded message is never a command. A command the bot does not know goes to the model as ordinary text.

There are no `/start` and `/help` commands. Telegram's command menu is set by the bot at every start.

"Admin chat" means the admin's private chat. Elsewhere the bot answers "This command is not available to you."

| Command | What it does | Who | Where |
|---|---|---|---|
| `/new` | Archives this conversation and starts a fresh session. Also clears a [tainted session](tools-and-safety.md#tainted-sessions). | Anyone allowed | Private chat and groups |
| `/sessions` | Lists your last 5 earlier conversations, newest first. | Anyone allowed | Private chat and groups |
| `/resume <n>` | Goes back to session `n` from `/sessions` and archives the current one. | Anyone allowed | Private chat and groups |
| `/status` | Shows how the node is doing: verdict, models, delivery, jobs, problems. | Admin | Admin chat |
| `/last` | Shows what the last turn did. | Admin | Admin chat |
| `/tasks` | Lists reminders and scheduled tasks, each with Details, Delete and Run now buttons. | Admin | Admin chat |
| `/memory` | Lists what the bot remembers, by topic. `/memory <topic>` shows one topic. | Admin | Admin chat |
| `/modules` | Lists modules. See [Modules](modules.md) for the verbs. | Admin | Admin chat |
| `/why` | Reply to a module's message with `/why` to see how it came about: the runs that led to it, each with when it ran, what started it, how long it took, how it ended, what it called and the names of the values it wrote. Never shows the module's own text. | Admin | Admin chat |
| `/cells` | Shows sensors and switches (cells) with their values. | Admin | Admin chat |
| `/secrets` | Lists secret names, dates and which modules use them. Never shows a value. `/secrets rm <module or node> <NAME>` removes one; `/secrets reset` drops all after a key mismatch. | Admin | Admin chat |
| `/metrics` | Sends metrics as a picture. Optional filter: `all`, `ganglion`, `runtime`, `process` or a word. | Admin | Admin chat |
| `/commands` | Shows the host-command switch. `/commands on` or `/commands off` flips it. | Admin | Admin chat |
| `/update` | Shows the running version and the newest release. `/update install vX.Y.Z` installs a signed release and restarts into it; `/update rollback` goes back to the previous version. The reply, and the notice of a new release, have Install and Roll back buttons that do the same. See [Updating](../running/operations.md#updating). | Admin | Admin chat |
| `/restart` | Stops the node cleanly so a supervisor starts it again. Settings outside modules apply then. | Admin | Admin chat |

`/reset` is an unlisted alias of `/new`.

If the node runs without a supervisor (a restart policy in your container platform, or systemd), `/restart` just stops it. See [Operations](../running/operations.md#restarting).

A private chat with someone who is not the admin gets no reply, so these commands matter only to the admin in practice. In a group, only `/new`, `/sessions` and `/resume` work.
