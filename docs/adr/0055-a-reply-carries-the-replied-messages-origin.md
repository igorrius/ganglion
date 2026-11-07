# ADR-0055: Let a reply carry the replied message's origin, from the node's own outbox

- **Status:** Proposed
- **Date:** 2026-11-07
- **Deciders:** Igor Artasevych
- **Spec:** [R-DLV-13, R-DLV-14](../spec.md#71-guaranteed-delivery),
  [R-POL-6](../spec.md#76-action-policies-and-risk-classes); design:
  [p1-reply-context-design.md](../plans/p1-reply-context-design.md)

## Context

The Telegram transport reads only the selected quote of a reply, so the agent does not know which
message the person answered, while a session can hold several topics (owner, 2026-11-06). The owner
wants the agent to know it for the bot's messages and their own, of any age, task reports included,
and wants the taint to follow where the replied message came from.

Facts that shape the choice:

- A scheduled task runs in a conversation of its own (`task:<id>`); its report reaches the chat but not
  the chat's session history, and may carry a web page's words.
- Today any selected quote taints the session (R-POL-6), even of the bot's own clean answer.
- The outbox keeps every sent message's body as the JSON of `event.Outgoing`, and the ref of its last
  part (`outbox.ref`, for edits), but only for 7 days: then the pruner deletes the row.
- Telegram's `reply_to_message` carries the replied message's id, author, date and text.

## Options

### A. The node's own outbox gives the origin; Telegram's copy only points

Every outgoing message records which session made it and whether that session was tainted. The
transport passes the replied message's ref; the agent looks the message up in the outbox by any part's
ref, and the recorded origin decides the note and the taint. What is not found falls back to
Telegram's copy as outside content.

Each part's ref is kept in a table of its own with a copy of what a reply needs: the message's text
(cut at 2,000 characters) and its origin, copied when the ref is recorded, so a reply reaches a
message for 90 days while the outbox keeps the message itself for 7. An edit with text replaces the
copy with the edit's.

### B. Telegram's copy, with "current session" decided by time

A message by the bot sent after the session started counts as the session's own and taints nothing;
an older one counts as outside content.

### C. The ref beside each answer in the session's history

The history keeps each answer's refs, so a reply points at one history item.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| A task report in the middle of a session | its own origin | taken as the session's own: a hole | not in the history: not found |
| A reply to an earlier session or a report | found, with its taint | always outside | not found |
| Storage | a field in the outbox body, one table of refs with a copy of text and origin | none | refs in the history, known only after an asynchronous send |
| Effort | moderate | small | large |

## Decision

**A.** It is the only option that knows where a task's report came from, and the node's own record,
not a copy a channel sends back, decides whether a text is clean.

The taint is decided before "current session": a message a tainted session wrote is wrapped as
outside content even when the reply comes in that same session, where it brings only an excerpt (the
history holds the rest), so an address in it never becomes known.

## Security

- **Trust boundary:** text the bot sent earlier comes back into a session. Its origin is read from the
  node's outbox, never from the channel's copy.
- **What an attacker controls:** a web page or a forward that entered some session; a group member
  replying to the bot (out of scope, outside content as today); Telegram's copy of a message.
- **A tainted session's answer replayed into a clean one** to act without confirmation: the outbox
  records the taint at send time, and a reply to such a message taints the session (code, not prompt).
- **A forged copy of a bot message:** the lookup is by ref in the node's own store, keyed by transport
  and conversation; a copy that does not match a row is outside content.
- **A forward the owner sent, replied to later:** outside content, whoever sent it.
- **A row from before the change, or a failed lookup:** tainted.
- **Residuals, accepted:**
  - An approved parked call that runs after `/new` stores what it sends with the new session's clean
    origin: the owner approved its text with the press.
  - A reply to a folded question reads its status line, the message's text, not the question kept in
    `Folded`.
  - Messages stored outside a turn (alerts, notifiers, the give-up reply) store no origin, so a reply
    to one counts as tainted: it costs a confirmation, never safety.

## Consequences

- A reply to the bot's own clean messages, and a quote of them, stops tainting the session.
- "Do it" in reply to yesterday's clean report works without a confirmation it never needed.
- Every outgoing message needs its origin set in one place; a kind that misses it counts as tainted,
  which costs a confirmation, never safety.
- A table of refs keeps a copy of each message's text, up to 2,000 characters a ref, for 90 days; the
  pruner drops older refs. A reply to an older message comes as outside content.

## Revisit when

Another transport gets replies; the owner needs replies to messages older than 90 days, or the copy
in the refs table outgrows the disk of a small node; or groups with several people come into scope.
