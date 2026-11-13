# Design note: Telegram groups, the addressing rule and named destinations

Notes from the owner's brainstorm of 2026-11-13. Nothing here is built. It
is the input for two ADRs, a spec change and an implementation plan; until
those land the spec still lists these items under
[Not now](../spec.md#14-not-now).

Marks: **owner** means the owner decided it in the brainstorm; **call**
means the author's default, open to the owner's correction.

## 1. Intent

The owner removed the old bot from the family's environment and moved the
production Telegram token to ganglion (owner, 2026-11-13). The old bot did
three things ganglion does not:

- it was reachable from a second chat, the family's forum group, under its
  own access settings;
- it answered there only when it was addressed;
- its scripts posted into a named topic of that forum. The owner set this
  up by saying so in the chat ("now write this to the family chat, topic
  such-and-such").

This note covers what is needed to replace that.

## 2. Goals, non-goals

Goals:

- A group is connected by the admin's press, with no id typed anywhere.
- In a connected group the bot answers every member, and only when it is
  addressed.
- A module can post to a named place, such as one topic of one forum, and
  only to places the admin gave it.
- Everything above is set up in words from the admin's private chat and
  ends up in the node's file.

Non-goals, all staying in "Not now":

- Roles beyond `admin` and `family`, with their own tools and filters.
  The group's `role` field is the seam they later plug into (section 4).
- Settings per topic (a language, a persona, limits).
- Reactions in groups.
- Reading a whole group through a classifier.
- Adding single people to the allowed list from the chat.

## 3. What exists today

- A forum topic is part of the conversation key
  (`telegram:<chat>/<thread>`), so each topic has its own session and a
  reply returns to its topic (R-DLV-13, `transport/telegram/conv.go`).
- Access is checked per person only (`access.List.Filter`). An allowed
  person is accepted in any chat; there is no allowed chat.
- Nothing checks whether the bot was addressed. `entities` are not read
  and the bot's username is not kept.
- A module's `Notify(text)` has no target and goes to the private chat of
  the admin who installed it
  ([ADR-0066](../adr/0066-notify-capability.md), R-RUN-16).
- The node already writes its own file on the admin's press: a module's
  line under `modules:` (`settings.SetModule`).
- The topic path was never checked against a live forum
  ([M2 plan](p1-m2-delivery-telegram.md)).

## 4. Access: a group carries a role

A connected group gives every member a role (owner). The rule for one
incoming message:

1. The sender is on the allowed list: their own role applies in a private
   chat.
2. The chat is a connected group: the group's role applies, **for
   everyone, the admin included** (owner: settings are not managed from a
   group).
3. Otherwise the message is dropped, as today.

The role is `family` for now: allowed and not an admin, so no tool that
changes the node is reachable from a group. When roles become
configurable, a group's line names another role and nothing else changes
(owner's direction; the roles themselves stay in "Not now").

The things that are private-chat-only today stay so: `/memory`, the memory
review, diagnostics, reactions.

A group that is not a forum has one shared session for all its members; a
forum has one per topic. The sender's name already travels with each
message.

## 5. The addressing rule

In a group, a message reaches the agent only when the bot is addressed
(owner's ruling of 2026-11-06, now taken into scope), whatever Telegram's
privacy mode is set to. Addressed means one of:

- the text or caption mentions the bot's username;
- the message replies to a message the bot sent, a module's message
  included;
- a command carries the bot's name: `/cmd@bot`. A bare `/cmd` in a group
  does not count (call: with several bots in a group it goes to all of
  them).

A button press under the bot's own message works as today.

Anything else is dropped in the transport, before the inbox: it is not in
the history, costs no model call and its text is not logged. A metric
counts the drops by reason.

The transport keeps the bot's username from `getMe` and reads `entities`
and `caption_entities`. A private chat is unchanged.

## 6. Connecting a group

The admin never acts in the group (owner). The node notices the group and
asks in the admin's private chat (owner):

1. The bot is added to a group, or someone addresses it in a group that is
   not connected.
2. In the group the bot answers once: it has been added but has no
   permission yet, and has asked its administrator (owner).
3. The admin's private chat gets a card: the group's title, its id, who
   addressed the bot, and two buttons, **Allow** and **Refuse**.
4. **Allow** writes the group's line to the node's file with the role
   `family` and the group works from the next message. With only one
   non-admin role there is nothing to choose; when more roles exist the
   card offers one button per role (call).
5. **Refuse** is remembered: the bot stays silent in that group and asks
   no more (call). The admin can still connect it later in words.

One card per group, never repeated while it is pending.

The admin can also say it in the private chat ("connect the Family
group", "disconnect it"): the agent picks the group from those the node
has seen and asks for the same press.

To notice being added, polling asks for `my_chat_member` as well.

## 7. Topics and named destinations

A destination is a name for a conversation: a private chat, a group, or
one topic of a forum. Names live in the node's file (owner).

The node remembers the titles of the topics it sees in connected groups:
every message in a topic names the topic's root, which carries the title.
So the admin says, in the private chat, "this module's reports go to the
K7 topic of the family chat", the agent finds the topic by title, proposes
a name (`family-k7`) and asks for a press.

When the node has not seen the topic yet, the agent says so and offers two
ways: address the bot once in that topic, or paste a link to any message
from it (the link holds the chat and the topic).

## 8. Modules: `NotifyTo`

- `Notify(text)` is unchanged: the owner's private chat.
- `NotifyTo(destination, text)` is new in the PDK. The module names a
  destination, never a chat id.
- A module may use only the destinations on its own line in the node's
  file. The admin gives them in words, with a press.
- The name is resolved when the message is sent, so pointing a name at
  another topic needs no rebuild.
- An unknown or ungiven name returns an error to the module, and the admin
  gets an alert in the private chat.
- A module's message in a group is marked with the module's name as
  elsewhere (ADR-0073, ADR-0074).

Three options were weighed: named destinations given per module (taken,
owner); redirecting a module's only recipient with no PDK change (one
address per module, so errors would land in the family chat); a raw
conversation key from the module (a module could then write to any chat
the bot is in, which breaks the boundary of ADR-0066).

## 9. The node's file

```yaml
telegram:
  groups:
    - chat: -1001234567890
      name: family
      role: family
  destinations:
    family-k7: telegram:-1001234567890/45
modules:
  - name: deye-report
    notify_to: [family-k7]
```

The node writes these sections the way it writes a module's line today,
each write after the admin's press, and applies them without a restart
(call; the plan checks how the running transport and the module host pick
the change up). M6 later gives these writes history and rollback together
with the rest of the file.

What the node has merely seen (groups, topic titles, pending and refused
requests) is not configuration and goes to its database (call).

## 10. Managing it in words

Admin-only tools, usable only in the admin's private chat, each ending in
a confirmation press:

- connect or disconnect a group;
- name a destination, rename it, remove it;
- give a module a destination or take it away.

When the agent builds a module that should post somewhere, it uses the
destination's name in `NotifyTo` and asks for the grant as part of the
install.

## 11. Security

- A group cannot change the node: its role has no admin tool, and the
  admin's own role does not follow them into a group. Text written by
  others in a shared history therefore cannot steer the agent into an
  admin action.
- Group membership is the access list of that group. Whoever the group's
  owner lets in can talk to the agent with the group's role; the Allow
  card says so.
- A module's reach is the list on its line, widened only by the admin's
  press, as every other right is (ADR-0031).
- The request card is written by the node, not by the model. A group's
  title is outside text and is shown as data.
- A stranger adding the bot to many groups gets one silent card each; the
  in-group answer is sent once per group. A cap on pending requests
  bounds it (call: 10, further ones are dropped and counted).

## 12. Checks

- The fake Bot API server (`telegramtest`) gains entities, the bot's
  username, `my_chat_member` and group titles.
- Unit tests: the addressing rule (each of the three forms and the drops),
  the access rule (the three cases of section 4), resolving a
  destination, a refused `NotifyTo`.
- Chatlab: connect a group through the card, a member is answered only
  when addressing the bot, a module posts to a topic.
- Live, in the owner's family forum: the same three, plus the topic check
  M2 could not run.

## 13. Documents to change

- **Spec:** move the items out of "Not now" (`notify` targets beyond the
  owner's chat; where a bot answers in a group; naming groups in the
  configuration), add the requirements, change R-RUN-16 and the 7.8 table.
  Configurable roles are added to "Not now" as the continuation.
- **ADR, new:** groups as an access unit and the addressing rule.
- **ADR, new:** named destinations for a module's messages. It supersedes
  ADR-0066 in what it says about the target.
- **Architecture:** the code map and the delivery page.
- **`ganglion.example.yaml`:** the new sections, and the wrong comment on
  the reply mode ("when addressed") corrected.

## 14. Open before the plan

- **The phase.** This changes the phase plan. It fits M7, whose inventory
  already names the old bot's topics, or a free number before M6. The
  cutover of task 7.5 has in fact happened ahead of M7. The owner decides.
- **A destination in a group that is later disconnected:** refuse the
  disconnect while a module uses it, or let the messages fail with an
  alert (call: the second, and the confirmation names the modules).
- **Scheduled jobs made in a group** keep reporting there today. Whether a
  `family` member may create one from a group is the existing tool rule
  and is not changed here.
