# ADR-0066: Let a module write to the owner's private chat through a `notify` capability with a daily limit

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-1, R-RUN-13, R-RUN-16), [7.1](../spec.md#71-guaranteed-delivery), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-6); [M4c design](../design/p1-m4c-reflexes-design.md) section 5; [ADR-0049](0049-three-gaps-where-outside-text-and-private-data-cross.md)

## Context

A reflex module has to tell the owner something ("battery 30 %, grid
off") without a model. Today a module can only write cells, return a
tool's answer to the model, and fail. The owner wants routine reactions
to cost no model turn (M4c design 1), and the text a module writes is
outside content for the model (R-RUN-13).

Two questions: where may a module write, and what does the model see of
it.

## Options

### A. The owner's private chat only

The message goes to the private chat of the admin who pressed the
install, the chat escalations already go to. The window says "Writes to
you: up to N messages a day".

### B. Targets set at install

`notify_to: telegram:channel:@my_power` in the module's config; the
window lists them; the node checks that each is an allowed chat.

### C. Both: the private chat by default, a list of allowed targets

And for the model:

### 1. The owner gets the text, the model a node note

### 2. The model gets the text as outside content (the session is tainted)

### 3. The model gets nothing

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| A module can write to someone the owner did not mean | No | If the check fails | If the check fails |
| Window text | One line | A line per target | Both |
| Needed now (development) | Yes | No | No |
| Groups and topics later | New ADR | Partly | Partly |

| Criterion | 1 | 2 | 3 |
|---|---|---|---|
| The model can explain an alert | Through cells and `module.status` | Yes | No |
| Taints the session | No | Yes | No |
| History stays honest | Yes | Yes | No: messages the model never knew of |

## Decision

**A and 1** (owner, 2026-11-10). During development the modules write
only to the owner; channels, groups and topics (the family chat, topic
"K7") come with multi-channel delivery and go to "Not now". The model
gets the note `[module] deye-alerts notified you at 14:30` without the
text, so the session stays clean and the model can still look at the
cells.

The node's rules (M4c design 5): the module's name as a prefix, plain
text with Markdown escaped, no buttons or previews, at most 1000
characters; `per_day` in the manifest under a node ceiling of 100, days in
the node's time zone; over the limit nothing is sent and the owner gets
one node line that day; the outbox key makes a repeated run send nothing
twice. `notify` is a module capability: scheduled tasks and the model
cannot use it.

## Security

- **Boundary:** module code → a person, through the transport.
- **What an attacker controls:** the module's code (written by a model)
  and the data it read from outside.
  - Phishing as the node or the agent → the node's prefix; plain text;
    no buttons or link previews.
  - Flooding the owner → `per_day`, the node's ceiling, one limit line a
    day.
  - Writing to other people → no target parameter exists; the chat is
    the installer's.
  - Outside text reaching the model through history → the model gets a
    fixed node note, never the text (R-RUN-13).

## Consequences

- The first reflexes need no model at all.
- The outbox gets a new source; history gets a node note kind.
- Other targets need a new ADR, with a check that the bot may and should
  write there.

## Revisit when

Multi-channel delivery lands, or a module needs to write to someone other
than the owner (a family group, a topic).
