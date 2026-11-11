# ADR-0073: A module's message records its module, and a reply to it names the module without its text

- **Status:** Accepted
- **Date:** 2026-11-11
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-13, R-DLV-14, R-DLV-15), [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-13, R-RUN-16, R-RUN-17); extends [ADR-0055](0055-a-reply-carries-the-replied-messages-origin.md) and [ADR-0066](0066-notify-capability.md); board card G-141

## Context

On 2026-11-11 the owner replied to a module's message ("Charging: 55 %,
3047 W") asking to add the time until the battery is full. The agent could
not say which module to change. Two facts met:

- A module's message was stored with the origin `system`, tainted, and no
  module name. The name stood only as a prefix in the message's text, which a
  reply brings back as outside content: the agent may not trust it, and the
  session is tainted by it. `module.write` refuses a tainted session
  (R-RUN-17), so the reply that asks for a change also closed the way to make
  it.
- The message had been sent by another node of the same bot (the local one),
  and the node that answered (Railway, a fresh volume) had no record of it.
  The agent was told only "a message", wrapped as outside content, with no
  word that its own node did not send it.

Changing a module by replying to what it wrote is the project's main loop
(the agent builds and rebuilds reflex modules on the owner's request), so the
node has to tell the agent which module wrote a message.

## Options

### A. The origin names the module; a reply brings the name and no text

A module's message is stored with the origin kind `module` and the module's
full name. A reply to it gets the node's own line: the module's short name
and the time, and a pointer to the module's source. The message's text, the
channel's copy and a selected quote stay out. The session is not tainted.

### B. The origin names the module; a reply brings the name and the text

As A, with the module's text wrapped as outside content after the line. The
agent sees the exact wording the owner points at. The session is tainted, so
`module.write` is refused until `/new`, or R-RUN-17 gets an exception for
this case.

### C. Leave the origin; teach the agent to find the module by the prefix

A directive tells the agent that `<name>: ` at the start of a replied text is
a module's name. No storage change. The name then comes from outside content,
which a forged or forwarded message can write.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| The agent learns the module from the node's record | yes | yes | no, from text |
| The reply leaves the session clean, so the module can be rewritten | yes | no | no |
| The module's text reaches the model (R-RUN-13 keeps it away) | no | yes | yes |
| The agent sees the exact message | no, it reads the source | yes | yes |
| Stopped by code, not by a prompt | yes | yes | no |
| Effort | one field, one branch | the same, and a rule for the taint | a directive |

## Decision

**A.** The name is the node's own knowledge, so it can be given as the node's
words, and keeping the text out is what leaves the session able to change the
module. It also keeps R-RUN-13 whole: a module's text never reaches the
model. What the module writes is in its source, which the agent reads with
`module.read`.

A message the channel says the bot wrote and this node's outbox does not hold
now says so in its note ("this node has no record of it: another node of this
bot may have sent it, or it is over 90 days old"). Its text stays outside
content and taints, as before.

## Security

- **Trust boundaries:** none new. The origin is written by the node when it
  stores the message and read back from its own outbox by the message's ref
  (R-DLV-14); the channel's copy still decides nothing.
- **What an attacker controls:** a module's text; a forged or forwarded copy
  of a message; a module's name only through an install the owner approved.
- **Abuse cases:**
  - *A module writes instructions in its message and the owner replies to
    it.* The text is not in the note; the quote the owner selected is not
    either. Test: `TestReplyToAModulesMessageNamesItAndStaysClean`.
  - *A forged message that looks like a module's.* Its ref matches no row, so
    it is outside content and taints, with the "no record" line.
  - *A name that carries text.* The name is checked with
    `modstore.CheckShortName`; one that fails makes the message one of an
    unknown source, wrapped and tainting. Test: `TestReplyNote`, "module, a
    name that is not one".
  - *An older node reads the new origin kind* (a rollback). An unknown kind
    is an unknown source: wrapped and tainting.
- The origin stays tainted, so any other reader of it treats the text as
  outside content.

## Consequences

- A reply to a module's message is enough for the agent to know what to read
  and rewrite; `module.write` works in that session.
- The agent does not see the message's wording and has to read the source.
  When the owner quotes a number from the message, the agent sees only what
  the owner typed.
- Messages sent before this change keep the `system` origin and come as
  before: outside content.
- The reply metric `ganglion.replies` has a new kind, `module`.
- Moving a node (workspace, installed modules, the outbox's refs) is not
  solved here: it belongs to M7.

## Revisit when

The owner needs the agent to see a module's exact message (then B with a
rule for the taint), or a module's messages go to people other than the
admin who installed it.
