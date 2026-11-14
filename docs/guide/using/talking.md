# Talking to the bot

## Who may talk to it

In a private chat, only the admin you set in `telegram.admin` gets answers. Anyone else gets no reply at all. There is no command to add more private-chat users today.

Other people reach the bot through groups that you allow.

## Roles

There are two roles.

- **admin**: you. In your private chat the agent can use tools, keep memory, run scheduled tasks and change the node.
- **family**: everyone in a group you have allowed, you included. They get plain conversation only: no tools, no memory.

The admin role holds only in your private chat. In a group, nobody can change the node, so words others write there cannot steer the agent into an admin action.

## Private chat and groups

In a private chat the bot answers every message.

In a group it answers **only when addressed**:

- you mention it (`@your_bot`),
- you reply to one of its messages, or
- you send a command with its name: `/new@your_bot`.

A bare `/status`, the bot's name typed without a mention, and the first message of a forum topic do not address it. Other messages are ignored and not stored. This does not depend on Telegram's privacy mode.

## Allowing a group

Add the bot to a group. The first time it is added or addressed there, it says once that it has no permission yet and sends you, in your private chat, a card with the group's title, id and two buttons: **Allow** and **Refuse**.

- **Allow** adds the group with the `family` role. It takes effect at once, with no restart.
- **Refuse** is remembered.

A request lasts 7 days and at most 10 can wait at once. To disconnect a group, ask the bot in your private chat; it asks you to confirm once.

## Topics in groups

In a group with topics (a forum), the node remembers the topic titles. In your private chat you can give a place a name:

```text
Call the Kitchen topic family-kitchen
```

Named places are called destinations. You can ask the agent to send a message to one, and a module can send its notifications to one. The names live in the `destinations` section of the configuration file.

## Replies and reactions

When an answer quotes the message it answers is set by `telegram.reply`: `auto` (in a group, or when newer messages came after it), `always` or `off`.

While a turn runs, the bot reacts to your message with an emoji. See the [configuration reference](../running/configuration.md#telegram).

## Language

The bot's fixed texts exist in English, Russian and Ukrainian. It uses your chosen language, or your Telegram language otherwise. The agent itself answers in whatever language you write.

Your Telegram language is the language of the app you wrote from last. If your phone and your computer run Telegram in different languages, the fixed texts switch with every device you write from. To keep one language, choose it: tell the bot, for example, "show your texts in English". An admin can choose this way; other people follow their Telegram language.
