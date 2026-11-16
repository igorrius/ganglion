# ADR-0091: Delete the owner's reminder or task without a press in a clean session

- **Status:** Accepted
- **Date:** 2026-11-16
- **Deciders:** Igor Artasevych
- **Spec:** [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-4, R-POL-6), [7.10](../spec.md#710-scheduler-and-reminders) (R-SCH-2), [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-14)
- **Issue:** [#226](https://github.com/igorrius/ganglion/issues/226)

## Context

An hourly reminder said "order a flashlight; tell me when it's done". The
owner replied "ordered" (live chat, 2026-11-16). The chat then got a card
with buttons for every scheduled job, a "Delete this reminder?" question
with Approve and Decline, and the model's text asking for the press. The
owner expected one short line.

Three things made it so:

- A reply to a reminder reached the model as "a reminder", with no job id
  (`event.Origin` held none), so the model called `schedule.list` to find
  the job.
- `schedule.list` sent every job to the chat as its own message with
  buttons on every call, even when the model only looked something up.
- `schedule.delete` is a side effect with the policy `confirm`. Since
  [ADR-0089](0089-set-a-reminder-without-a-press-in-a-clean-session.md),
  creating a reminder in a clean session needs no press, so stopping one
  asked for more than starting it.

The first two are fixes with no policy in them. The third changes what
needs a press, so it is decided here. The precedent is ADR-0089's rule:
in a clean session `schedule.create` of a reminder runs at once, in a
tainted one it asks (policy rule 7). System jobs and module jobs cannot
be deleted at all (`schedule.ErrSystem`), whoever asks.

## Options

### A. `schedule.delete` runs without a press in a clean session, for reminders and tasks

A policy rule: `schedule.delete` is auto in a session that is not
tainted and asks in a tainted one. The store still deletes only the
caller's own job and refuses a system or module job. The tool answers
with one short "🗑 Off: `<text>`" line.

### B. Only a reminder is deleted without a press

As A, but deleting a task still asks. The rule would have to read the
job from the store to know its kind, since the call carries only the id.

### C. Delete without a press, with an Undo button

As A, and the "Off" line carries Undo, which brings the job back.

### D. Keep the press, and add only the Done button

A recurring reminder's message carries "✅ Done", which deletes it at
once; a delete in words still asks.

## Comparison

| Criterion | A | B | C | D |
|---|---|---|---|---|
| "ordered" in reply to a reminder: one line, no question | yes | yes | yes | no |
| "stop the morning report" (a task): no question | yes | no | yes | no |
| An injected page can delete a job without a press | no: tainted asks | no | no | no |
| The policy decides from the call alone | yes | no: reads the store | yes | yes |
| A mistaken delete is undone | by asking again | by asking again | in one press | n/a |
| New code | a rule | a rule and a store read in the policy | a rule, a restore in the store, a button | a button |

## Decision

**A.** Deleting the owner's own job takes away only what the owner set up,
in the owner's own words, and a clean session holds nothing but those
words and the node's results. A task differs from a reminder in what it
does when it runs, not in what stopping it does, so B keeps a press with
no reason the owner can see and moves a store read into the policy. C
adds a restore path for a case the owner did not meet; it stays open if
mistaken deletes appear. D leaves the reported chat as it was whenever
the owner answers in words.

The rule:

- `schedule.delete` in a session that is not tainted: auto.
- In a tainted session the side-effect rule asks, so a page or a forward
  still cannot stop a job without the owner.
- System and module jobs are refused by the store whatever the policy
  says; someone else's job is not found.
- The tool's answer to the model is the one line the owner gets,
  "🗑 Off: `<text>`" in the owner's language; the model answers with it
  and nothing more.

The fixes that come with it (owner, 2026-11-16, in the issue):

- A reminder's message stores its job id in its origin (`event.Origin.Job`),
  and the history note of a reply to it names the job, so the model
  deletes it directly.
- A recurring reminder's message carries "✅ Done". The press deletes the
  job and edits the message to say it is off, with no model turn; it is
  the owner's own press, as Delete on a listed job is. A one-time
  reminder gets none: it is over once sent.
- `schedule.list` sends its cards only with `show: true`, when the owner
  asked to see the list; otherwise the model gets the text and nothing
  goes to the chat.

## Security

- **Trust boundary:** the policy for a side effect in a clean session,
  which was "the owner presses" and becomes "the owner's own message", as
  in ADR-0089.
- **What an attacker controls:** text that enters the session (a page, a
  forwarded message, a reply to someone else's message) taints it, and
  the rule then asks. The job id in a reply's note comes from the node's
  own outbox row, never from the channel's copy of the message.
- **Abuse cases:** a page asking to stop the owner's daily report: the
  session is tainted, so it asks. A model that misreads the owner and
  deletes the wrong job: the "Off" line names the job, and the owner sets
  it again in words. A call naming a system or module job: the store
  refuses it. A call naming someone else's job: not found. A scheduled
  task deleting jobs: no grant may name a `schedule.*` tool, so the grant
  rule refuses it first.

## Consequences

- "ordered" in reply to a reminder takes one line and no press; so does
  "stop the morning report".
- A recurring reminder can be stopped from its own message in one press.
- `schedule.list` no longer floods the chat when the model only looks.
- A deleted job is gone; bringing it back means setting it again.
- A new policy rule with tests, a field of `event.Origin`, a quick
  action, texts in every language, the tools' descriptions and the
  guide.

## Revisit when

A job is deleted that the owner did not mean to stop, or the owner asks
for Undo.
