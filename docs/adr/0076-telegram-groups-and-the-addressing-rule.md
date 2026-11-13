# ADR-0076: Connect a Telegram group by the admin's press and answer in it only when addressed

- **Status:** Proposed
- **Date:** 2026-11-13
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-16, R-DLV-17),
  [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-10),
  [7.8](../spec.md#78-security-and-operations-mvp),
  [14 Not now](../spec.md#14-not-now);
  [design](../plans/telegram-groups-design.md), [plan](../plans/telegram-groups.md)

## Context

The owner moved the production bot's token to ganglion on 2026-11-13 and
removed the old bot. The old bot was reachable from the family's forum
group and answered there only when addressed. Ganglion cannot replace that
yet:

- Access is a list of people (`allowed`). An allowed person is answered in
  any chat, and nobody else is answered anywhere. There is no allowed chat.
- Only the admin from the config is ever on the list: nothing adds a
  person.
- Nothing checks whether the bot was addressed. A bot that is a group's
  admin, or whose privacy mode is off, receives every message, and the node
  answers each one that an allowed person wrote.
- A forum topic is already part of the conversation key, so a topic has its
  own session and replies return to it (R-DLV-13).

The owner's ruling of 2026-11-06, until now in "Not now": in a group or a
forum the bot answers only when it is itself addressed, whatever
Telegram's privacy mode says, and groups are named in the configuration of
the channel. The owner's rulings of 2026-11-13: every member of a connected
group may talk to the bot; a group carries a role; the node's settings are
not managed from a group; a group is connected by a press on a card the
node sends to the admin's private chat.

## Options

### A. A listed group gives its members a role; the transport drops what is not addressed; the admin connects a group by a press

`telegram.groups` in the node's file lists group chats, each with a role.
A message in a group is first checked by the transport: it goes on only
when it mentions the bot, replies to the bot's message or is a command
with the bot's name. The access filter then passes it when its group is
listed, whoever wrote it. A group that is not listed makes one request:
the node says once in the group that it has no permission yet, and sends
the admin a card with Allow and Refuse. Allow writes the group's line.
The admin role holds only in the admin's private chat.

### B. People are listed one by one; a group needs the group and the person

The group is listed and each member too. Stricter, and it needs a way to
add a person, which does not exist.

### C. Keep the list of people and trust Telegram's privacy mode for addressing

No code for addressing: with privacy mode on, Telegram itself delivers
only mentions, replies and commands.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| The family chat works after it | Yes | Only for the owner, until people can be added | Only for the owner |
| Holds when the bot is a group's admin or privacy mode is off | Yes | Yes, if it has A's rule | No: every message of an allowed person is answered |
| Who decides who may talk | The admin, per group; the group's owner, per member | The admin, per person | The admin, per person |
| Cost of a stranger in a connected group | A model call per addressed message, under the daily limits | None | None |
| New code | The rule, a file section, a request store, a card | A's, plus adding people | None |
| Fits the owner's rulings | Yes | No ("every member") | No ("whatever the privacy mode says") |

## Decision

Option A. It is the only one that makes the family's forum work for the
family and keeps the rule in the node's own code, where a setting on
Telegram's side cannot switch it off.

Details that follow from the rulings:

- A group's line names a role; only `family` exists. Roles with their own
  tools are a later decision, and the line's `role` field is where they
  plug in.
- The admin role does not follow the admin into a group (R-POL-10).
- A bare `/cmd` in a group does not address the bot: with several bots in
  a group it goes to all of them.
- A mention counts by Telegram's entity, never by the bot's name found in
  the text.
- A message that does not address the bot is dropped in the transport: it
  is not stored, not logged with its text and costs no model call.
- A refusal is remembered. A request that nobody answers lasts 7 days. At
  most 10 requests wait at once.

## Security

- **Trust boundaries.** A new one: a group chat's members, who are not on
  the node's list, reach the agent. The request card crosses from a group
  chat (anyone) into the admin's private chat.
- **What an attacker controls.** A member of a connected group: the text
  of their messages, and with it the shared history of that group's
  session. A stranger: which groups the bot is added to, and those groups'
  titles and their own display name.
- **Abuse cases and what stops them.**
  - *Steering the agent into an admin's action through a group's shared
    history.* No turn that a group's message or press started has the admin role
    (`setAdmin` reads the group the transport put on the event), so no
    admin tool is on offer there, whoever writes.
  - *Talking to the bot from a group nobody allowed.* The access filter
    drops every event of a group that is not listed, the admin's own
    included.
  - *Flooding the admin with cards by adding the bot to many groups.* One
    request per group, none after a refusal, at most 10 waiting; the rest
    are dropped and counted.
  - *A title or a name that is an instruction or Markdown.* The card is the
    node's text with no model; the title and the name go in escaped, never
    into a log line above debug, never into a metric label, and never into
    the note the model reads about the press.
  - *A press on Allow by someone else, or after the admin lost the role.*
    The button belongs to the admin and their private chat (the pending
    store refuses others), and the action checks the role again.
  - *Faking an address.* The bot's name typed without an entity, a mention
    of another account, an entity that points outside the text and a
    topic's root written by the bot do not count.
  - *Spending the node's budget from a connected group.* The existing
    daily limits apply; the Allow card says that everyone in the group will
    be able to talk to the bot.

## Consequences

- The family's forum can use the bot after one press, with a session per
  topic.
- An allowed person is no longer answered in a group that is not
  connected. That is a change of behaviour.
- The admin has no tools in a group, and neither has anyone else:
  `family` gets conversation only (spec 7.8). A group that needs tools
  waits for roles.
- Built because of this: the addressing rule in the transport,
  `telegram.groups` and its writer, the `groups` package (requests,
  connected groups, the card's buttons), the group rule in the access
  filter, the card in the agent.
- Not built here: disconnecting a group in words, topic titles, named
  destinations for modules. They are the second plan and its own ADR.

## Revisit when

- A group needs tools, memory or its own persona: that is the roles and
  per-group settings of "Not now".
- A second chat transport arrives: the addressing rule and the group key
  are the transport's, the filter and the requests are not.
- Reading a whole group through a classifier is taken up: it replaces the
  drop in the transport for groups that opt in.
