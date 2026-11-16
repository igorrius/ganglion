# ADR-0088: Let a newer question replace a waiting one of the same tool from an earlier turn

- **Status:** Accepted
- **Date:** 2026-11-16
- **Deciders:** Igor Artasevych
- **Spec:** [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-2, R-POL-9)
- **Issue:** [#214](https://github.com/igorrius/ganglion/issues/214)

## Context

A call that needs the owner's press is parked with a question and two
buttons ([ADR-0032](0032-buttons-and-resume.md)). The question stays valid
for its `ttl` (`pending.DefaultTTL`, 15 minutes); only the expirer closes
it.

Live chat, 2026-11-15: the owner asked for a reminder, the agent asked to
confirm it for one time, the owner asked for another time, and the agent
asked again. The first question kept its Approve and Decline buttons. A
press on its Approve within the `ttl` would have run the old call: two
reminders, at the old time and at the new one. The owner pressed it later
and got "Expired", but the chat showed a live-looking question that no
longer meant anything.

Nothing in the node knows that a question is out of date: the model asks
again, and the old call simply waits. The owner wants the old question
closed as soon as a new one stands in its place, its buttons gone and the
record kept.

## Options

### A. A newer question of the same tool from a later turn replaces it

When a call is parked, every call of the same tool, the same person and
the same conversation that still waits and was parked in an earlier turn
is withdrawn: its status becomes `replaced`, its buttons are spent, and
its question folds under a line "Replaced by a newer question, `<time>`"
(R-POL-9). Calls of one turn stand side by side.

### B. Any new message of the owner withdraws all waiting questions

A message in the conversation closes every question of that conversation
that still waits.

### C. The model withdraws a question with a tool

A tool, say `confirm.withdraw`, closes a question by id when the model
decides it is out of date.

### D. Keep it as it is and shorten the `ttl`

## Comparison

| Criterion | A | B | C | D |
|---|---|---|---|---|
| The flashlight case is fixed | yes | yes | when the model remembers | partly |
| A question the owner still means to press survives "wait, and also…" | yes, unless it is the same tool | no | yes | yes |
| Two questions asked in one turn (two host commands) both stay | yes | yes | yes | yes |
| Decided in code, not by the model | yes | yes | no | yes |
| Cost | a status, a column, an edit | an edit per message | a tool and prompt words | a number |

## Decision

**A.** The same tool asked again after the owner spoke is, in practice, the
same request changed: a reminder for another time, a message reworded, a
command with another flag. Limiting it to an earlier turn keeps the case
where the model asks two things of one tool at once. B closes questions
the owner still wants, C leaves a safety property to the model, and D
only narrows the window in which the old Approve runs.

How it works:

- `pending.Store` gets a status `replaced` and a column `replaced_by`.
  When the agent gets a newly parked call (a tool result with a pending id
  and a question), it calls `Replace(newID)`: in one immediate
  transaction every waiting call of the same tool, principal and
  conversation from another turn, parked before the new one, becomes
  `replaced` with `replaced_by` the new id and `answered_ms` now, and its
  buttons are spent. `Replace` returns all calls whose `replaced_by` is
  the new id, so a rerun of the turn gets the same set back. Their
  request cells are answered no, by `replaced`.
- The agent removes the replaced ids from the session's pending list and
  folds each question: an edit of the question's message, found by its
  key as an expired question is, with the line "Replaced by a newer
  question" and its time over the question in a collapsed quote. A
  question still in the same turn's output (not sent yet) is dropped
  from it instead.
- A later press on a replaced question's button runs nothing: the toast
  says the question was replaced, and the message folds as above.
- The expirer only expires `waiting` calls, so a replaced call never
  resumes the conversation.

## Security

No new boundary. The change only removes a way for a call to run: a
question the owner can no longer see as current cannot be approved.
What an attacker controls is unchanged; a tainted session still parks
every side effect, and a replacement is decided in code from stored
fields (tool, principal, conversation, turn), not from anything the model
or a page says.

## Consequences

- A changed request leaves one live question in the chat, and the old one
  says why it closed.
- A question of the same tool that the owner did still want, asked in an
  earlier turn, closes when the model asks that tool again; the owner
  asks again. The model is told in the new call's result that it replaced
  an earlier question.
- One migration (`replaced_by`), one status, one toast and one line in
  every language of `texts`.

## Revisit when

The owner loses a question they wanted to press because a different call
of the same tool replaced it.
