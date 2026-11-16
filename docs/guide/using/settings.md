# Settings from the chat

Some settings of the node's file, `ganglion.yaml`, can be changed by asking the agent in the admin's private chat. Every change, from the chat or not, is recorded, so you can ask what changed and roll a change back.

## Changing a setting

Ask in words: "raise the daily limit to 300 turns", "answer in English and Ukrainian", "log at debug level".

1. The agent shows a card: each setting with its old and new value, and the reason. If the conversation holds content from outside (a web page, a forwarded message), the card says so: check the change is what you asked for.
2. Press **Approve** to write the change, or **Deny**. A card you leave alone expires, and nothing is written.
3. The node checks the change again when you press. A value the node would not start with is refused with the reason, and the file stays as it was.
4. The change takes effect after a restart. The reply has a **Restart now** button that does what [`/restart`](commands.md) does. You can make several changes and restart once.

## What can be changed from the chat

| Setting | What it is |
|---|---|
| `node.tz`, `node.memory_limit` | Time zone; the soft memory limit |
| `log.level` | How much the node logs |
| `telegram.reply`, `telegram.rich`, `telegram.reactions.after`, `telegram.reactions.dwell` | How replies look in Telegram |
| `agent.languages` | The languages the agent speaks |
| `agent.session_idle`, `agent.call_timeout`, `agent.turn_timeout` | When a conversation goes idle; how long a model call and a turn may take |
| `agent.limits.daily_budget_usd`, `agent.limits.daily_turns`, `agent.limits.daily_tokens` | Daily limits |
| `agent.search.mode` | Web search on or off |
| `memory.prompt_budget`, `memory.review.enabled`, `memory.review.quiet` | How much memory goes into a prompt; the memory review |
| `update.check` | The daily check for a new release |
| `models.purposes` | Which of the models you defined answers for each purpose |

The [configuration reference](../running/configuration.md) describes each setting.

## What stays on the host

Everything else is changed by editing the file on the machine, and the agent says so if you ask:

- **Secrets** (the bot token, model keys, the search key): the agent never handles them. Use `ganglion secret set` or the file.
- **The admin** (`telegram.admin`) and **where the bot talks to Telegram** (`telegram.api_url`): changing them from the chat would hand the node over.
- **Model providers and model definitions**: they name the addresses keys are sent to and the commands the node runs.
- **The sandbox, the public address, the metrics address, the log folder and the workspace**: they widen what the node exposes or move its data.

Some settings have their own tools and cards instead: modules ([Modules](modules.md)), where messages go and connected groups (the agent's routing tools), and polling or webhook ([Telegram: polling or webhook](../running/telegram-updates.md)).

## History and rollback

- Ask "what changed in the settings?" to see the latest changes: when, who made them (you through the agent, a rollback, a module or routing tool, or an edit on the host) and the reason.
- Ask "roll that back" to undo the latest change of a setting from the table above, or name a change from the history. The agent shows the same card with the old values. Parts of a change it cannot roll back here (a module, a route, a host edit) are listed on the card.

The history lives in the agent's workspace, in `config/ganglion.yaml` of its git repository. An edit of the file on the host is recorded at the next start. A node without git still takes changes from the chat but keeps no history.
