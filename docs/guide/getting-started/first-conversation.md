# Your first conversation

## Run the node

From source:

```sh
export TELEGRAM_BOT_TOKEN=...
./bin/ganglion run
```

In a container, the image already runs `ganglion run --db /data/ganglion.db`.

The node prints log lines as it starts. Keep it running. Only one process may poll a bot token at a time, so stop any other program that uses the same bot.

## Say hello

Open a private chat with your bot in Telegram and write a message. Only the admin you named in `telegram.admin` gets answers. Everyone else gets no reply.

There is no `/start` or `/help` command. Just write.

## The introduction

On a fresh node the agent has no persona yet. It starts a short introduction: it asks your name, how you want it to talk, and in which language. Then it proposes a persona and waits for you to approve it with a button. The persona is saved as plain Markdown files in the `workspace/persona/` folder, which you can edit later.

## Ask for something simple

Try a question or a small task:

```text
What is the weather forecast for Lisbon tomorrow?
```

The bot shows a reaction emoji while it works, then answers. A web search needs no button. See [Tools and safety](../using/tools-and-safety.md) for what needs a press.

Send `/status` to see how the node is doing. See [Commands](../using/commands.md).

## Ask for a small automation

Describe a routine task in your own words:

```text
Every 30 minutes read the temperature from https://example.com/api/temp
and tell me if it drops below 5 degrees.
```

The agent picks one of two templates (a reader for the outside service, a reflex that reacts to the values), writes the code, builds it in the background and shows you a live card.

When the build is done, you get an install window. It lists, in plain words:

- the network hosts the module may reach,
- the secrets it needs,
- the cells it creates and reads,
- when it runs,
- how many messages a day it may send you.

Press **Approve** to install or **Deny** to drop it. The question folds to a line such as "Approved, 21:09" once you decide. If you do nothing it expires.

After the install, `/modules` lists the module and `/cells` shows its values. More in [Modules](../using/modules.md).
