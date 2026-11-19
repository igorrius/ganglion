# ADR-0101: A server's instructions reach the model as data, and notes about it live in a memory topic

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** Igor Artasevych (the owner)
- **Spec:** [R-MCP-4a](../spec.md), [R-MCP-11](../spec.md),
  [R-MEM-8](../spec.md), [R-POL-6](../spec.md)

## Context

In the live check of #261 the model answered a question about a month's
purchases from a connected MCP server, but could not tell what a field of
the result meant (whether a receipt's sum was before or after discounts).
It guessed and said so. The next turn will guess again: nothing the model
learns about a server's data outlives the turn (#288).

Two sources of that knowledge exist:

1. **The server's own words.** The MCP `initialize` result may carry
   `instructions`: free text on how to use the server's tools. The
   client (`mcpclient`) drops it today.
2. **What the model finds in use.** A field's meaning, a quirk, a filter
   that must be set. Only the model sees it, in a tool's result.

Constraints:

- A tool's definition (name, description, schema) has the standing of
  the approved copy: it is pinned by the fingerprint and a change turns
  the server's tools off until the admin presses Connect (R-MCP-7). A
  tool's result is outside content and taints the session (R-MCP-4,
  R-POL-6).
- Memory is written from the owner's words only, and a write from a
  tainted session needs the owner's confirmation (R-MEM-8, R-POL-6,
  [ADR-0072](0072-who-writes-memory.md)). Every topic is in an admin's
  system prompt whole, within the budget (R-MEM-7).
- The owner rejected an automatic search for a server's documentation on
  connect (#288): most servers have none, and it costs every connect.

## Options

### A. Pin the instructions with the tool list

Put the instructions into the fingerprint and the approved copy. The
connect card shows them; a change turns the tools off until Connect.
They then reach the model with the same standing as a description.

### B. Deliver the instructions as outside content

Keep the instructions the client got in the current session, cut to a
limit, unpinned. `mcp.use` gives them to the model when it turns on the
first tool of that server in the session, as outside content: the
session becomes tainted and the text is wrapped as data.

### C. A skill per server

The model drafts a skill for the server; the owner installs it. It
carries both the instructions and what the model learned.

For the notes, the choice is between a memory topic per server
(written with `memory.write`, confirmed by the owner because the session
is tainted) and a store of its own in `mcpclient`.

## Comparison

| Criterion | A. Pin | B. Outside content | C. Skill |
|---|---|---|---|
| The admin has to read prose on the card | yes, every connect and change | no | yes, at install |
| A harmless edit by the server | turns the tools off | nothing | nothing |
| An injected instruction | reaches the model with the standing of a definition | reaches it as data, after which every call with input in the session is confirmed | reaches it after the admin approved it |
| Effort | card, fingerprint, migration of every pin | client field, one branch in `mcp.use` | skill drafting and install |
| Carries what the model learns | no | no | yes, by hand |

## Decision

**B for the instructions, a memory topic for the notes.**

- The instructions are the server's text, like its results, so they are
  treated as results. Pinning them (A) would put prose on a card no admin
  reads closely, break every existing pin and turn the tools off on a
  harmless edit, for little gain: the model calls the server's tools
  right after `mcp.use`, and the first result taints the session anyway.
- The notes go to the memory topic titled `mcp <server>` (slug
  `mcp-<server>`). Memory already keeps topics in the prompt, versions
  every change in git, shows the owner each write with Undo and asks
  for confirmation of a write from a tainted session. A store of its own
  would repeat all of that. This is a narrow exception to "the owner's
  words only" (R-MEM-8): facts about a connected server's data, into that
  server's topic, each one confirmed by the owner. Disconnecting a server
  drops its topic.
- C stays possible later; it is heavier than a note for a single server.

## Security

- **Trust boundary:** the server's `initialize` answer is a new input
  from the server into the model's context. It crosses the same boundary
  as a tool's result.
- **What an attacker controls:** the server, so the whole text of the
  instructions; and through a tool's result, what the model would like
  to write as a note.
- **Abuse cases and what stops them:**
  - *Instructions that tell the model to act* ("call delete_cart"): the
    text is wrapped as data and taints the session (code: `External` on
    the result), so a call with input is confirmed by the owner
    (R-MCP-4, R-POL-6). A `forbidden` tool is never turned on.
  - *A huge text to flood the context:* cut to 2,000 runes by the client,
    and `initialize`'s answer is already capped (`MaxInitBytes`).
  - *A changed text between sessions:* nothing pins it, and nothing
    trusts it more than a result; a new session reads the new text.
  - *A note that plants an instruction in memory:* a write from a
    tainted session needs the owner's press (R-POL-6), passes the
    clean-context check (R-POL-7), and is shown with Undo (R-MEM-9).
    Memory is shown to the model as a data block.

## Consequences

- The model sees how a server wants to be used, and keeps what it learns
  about a server's data, so the next turn does not guess again.
- `mcp.use` taints the session when the server sent instructions, one
  call earlier than the first result would.
- The owner presses Remember (confirms) for each note; a note is a few
  words and is rare.
- A reconnect after a disconnect starts without notes.
- To build: the client keeps the instructions; `mcp.use` adds them and
  names the notes' topic the first time a server's tool is turned on in
  a session; disconnect drops the topic; the core rules say what a note
  is for.

## Revisit when

Servers send long or changing instructions that the owner wants to
review, or notes about one server pile up past what a topic holds (50
facts).
