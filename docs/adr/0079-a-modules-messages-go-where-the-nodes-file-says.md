# ADR-0079: Route a module's messages by the node's file, to named destinations

- **Status:** Accepted
- **Date:** 2026-11-13
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-16, R-RUN-18),
  [7.14](../spec.md#714-node-configuration) (R-CFG-12),
  [14 Not now](../spec.md#14-not-now);
  [design](../design/telegram-groups-design.md),
  [plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/telegram-destinations.md); changes the target decided in
  [ADR-0066](0066-notify-capability.md); board card G-153

## Context

A module's `notify` goes to the private chat of the admin who installed
it (ADR-0066, R-RUN-16). The old family bot's scripts posted into a topic
of the family's forum, and the owner set that up by saying so in the chat.
ADR-0066 deferred this: "revisit when … a module needs to write to someone
other than the owner (a family group, a topic)".

Since [ADR-0076](0076-telegram-groups-and-the-addressing-rule.md) a group
can be connected. The first hour of use showed the gap (G-153): asked in
the private chat to post the battery's state to "Family / K7", the agent
could name only the forum as a whole, which is no conversation the node
knows, and it does not know a topic's title or id at all.

The owner's words (2026-11-13): "I need to configure the channels a module
writes to, maybe more than one"; later the routing moves onto an event
bus; settings are managed by the admin in words.

Facts from the code: the Rust glue between a module and the node is copied
into every module (there is no shared crate), two modules run in
production, and the node already reloads a module when its line in the
file changes.

## Options

### A. The node's file routes: a module's line lists named destinations

`destinations:` in the node's file names conversations (`family-k7` is a
forum and a topic). A module's line may carry `notify_to: [family-k7,
owner]`; `owner` is built in and means the private chat of the module's
owner. The module calls `notify(text)` as today and does not know where
the text goes: the node sends it to every destination on the line.

### B. The module routes: a new `notify_to(destination, text)` host function

The module names a destination per message; the node checks the name
against the module's line. One module can send its report to the family
and its errors to the owner.

### C. A raw conversation key from the module, or in its config

ADR-0066's rejected option B: the module, or its own settings, holds
`telegram:-100…/45`.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| The owner changes where a module writes, in words, with no rebuild | Yes | Only among the names the code already uses | No |
| More than one place | Yes, every message to all of them | Yes, chosen per message | Yes |
| Different messages to different places | No | Yes | Yes |
| Change to the module ABI, the template and the two running modules | None | A new host function, glue in every module | None or a config key |
| A module knows the owner's chats | No | Their names | Their ids |
| Fits an event bus later (a module publishes, the routing is outside) | Yes: the same shape | Has to be turned round | No |
| A module can write where the admin did not allow | No | No | Yes |

## Decision

Option A (owner, 2026-11-13). The owner asked for configuring where a
module writes, and A is exactly that at a quarter of B's size, with no
change to modules that already run. When one module must split its
messages, the next step is a kind on the message that the file routes by,
which is still A's shape.

Details:

- A destination's name is lower-case letters, digits and dashes; `owner`
  is reserved. A destination is a private chat on the access list, a
  connected group, or a topic of one.
- With no `notify_to` a module writes to `owner`, as before.
- A name is resolved when a message is sent, so pointing a name at another
  topic needs no reload. A line's list is read when the module loads, and
  changing it reloads the module, as changing its config does.
- The daily limit counts a module's messages, not their deliveries. The
  limit line and the chain line go to the owner only.
- A message to a destination other than the owner's chat leaves no note in
  that conversation's history and does not roll its session over: "the
  module notified you" is the owner's note.
- The node remembers the titles of the topics it sees in connected groups,
  so the admin names a topic by its title.
- Destinations, a module's list and disconnecting a group are managed by
  admin tools, each with a confirmation; the admin role exists only in the
  private chat (R-POL-10).
- The owner never has to name a destination (owner, 2026-11-13, after the
  first live try took three confirmations and named the whole forum
  "family-k7"). `module.notify_to` and `message.send` take a place
  directly: the key of a connected group or of one of its topics, or the
  link Telegram gives for a topic (`https://t.me/c/<id>/<topic>`). One
  request is one call and one confirmation; the node writes the
  destination behind a place itself, named by the chat's and the topic's
  numbers, since a title is outside text and may be in any script.
- Telegram's Bot API has no method that lists a forum's topics (checked on
  2026-11-13), so a topic nobody wrote in since the node started is
  unknown; the agent then asks for a message in it or for its link, and
  never falls back to the group's own key. A built-in skill,
  `telegram-chats`, tells the agent all of this.
- A destination that a module uses cannot be removed, and a group that a
  destination points at cannot be disconnected: the tool names what holds
  it.

## Security

- **Trust boundaries.** A module's text, which is outside text, now
  reaches a group chat's members instead of the owner alone.
- **What an attacker controls.** A module's text (a module reads the
  network); a topic's title and a group's title (any member).
- **Abuse cases and what stops them.**
  - *A module writing where nobody allowed.* A module has no way to name a
    place: only the admin's line does, and it holds names the admin
    defined. A name that no longer resolves sends nothing and is counted.
  - *A module's text as an instruction to the agent in the group.* The
    text never enters a history (R-RUN-13 holds); in a group not even the
    node's note does. A reply to it names the module and not its text
    (R-DLV-15).
  - *A module's text as formatting, a link preview or a button.* Unchanged:
    escaped, no preview, no buttons, cut at 1000 characters, signed by the
    node.
  - *Flooding a group.* The module's daily limit bounds messages; each goes
    to at most the destinations on its line (capped at 8).
  - *A topic's title as an instruction.* Titles reach the model only as a
    tool's result marked as outside content; a question the admin confirms
    shows the title escaped.
  - *Changing the routing from a group.* The tools are the admin's, and
    nobody is an admin in a group.

## Consequences

- The owner says "send the battery alerts to K7 and to me", presses once,
  and it holds from the next message.
- `message.send` can post to a topic by its destination's name, which
  closes G-153.
- A module cannot send different messages to different places yet.
- Built: `destinations:` and `notify_to` with their writers, topic titles
  in `groups`, the fan-out in the module host, five admin tools.
- ADR-0066 stays as it is except for the target: R-RUN-16 now points here.

## Revisit when

- A module needs different messages in different places: add a kind to
  the message and route by it in the file.
- The event bus arrives: `notify` becomes a publish, and a module's
  `notify_to` a subscription.
- A second chat transport arrives: a destination's key already names its
  transport.
