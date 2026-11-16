# ADR-0089: Set a reminder without a press in a clean session, with a Delete button

- **Status:** Accepted
- **Date:** 2026-11-16
- **Deciders:** Igor Artasevych
- **Spec:** [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-4, R-POL-6), [7.10](../spec.md#710-scheduler-and-reminders) (R-SCH-2)
- **Issue:** [#215](https://github.com/igorrius/ganglion/issues/215)

## Context

`schedule.create` is a side effect with the policy `confirm`, for both
kinds of job. "Remind me tomorrow to buy a flashlight" therefore asks for
a press before anything is set. The owner finds that one step too many
for so plain a request (live chat, 2026-11-15).

The two kinds differ:

- A **task** runs a full model turn unattended, spends tokens and works
  within a grant fixed at creation (R-POL-8). R-SCH-2 names these as the
  reason to confirm.
- A **reminder** is a fixed text sent at its time to the conversation it
  was asked in. It has no grant (`schedule.create` refuses one), runs no
  model and reaches no one else.

[ADR-0031](0031-agent-security-baseline.md) foresaw this: "if the owner
finds the bot tiring, the presses are the first thing to look at". A
precedent exists: `memory.write` and `language.set` run without a press in
a clean session and confirm in a tainted one (policy rule 7).

## Options

### A. A reminder runs without a press in a clean session, and its message carries Delete

A policy rule: `schedule.create` with `kind` `reminder` is auto when the
session is not tainted; in a tainted session the side-effect rule asks as
today. The created reminder is shown to the owner as a message, "⏰
Reminder set: `<when>`, `<text>`", with the Delete button a listed job
has. Tasks keep their confirmation.

### B. Only a one-time reminder runs without a press

As A, but a recurring reminder (`cron`) still asks.

### C. Keep the press

### D. Make the press optional by a setting

A per-tool override of the policy (`policy.Config.Overrides`, not read
from `ganglion.yaml` yet) would let the owner set `schedule.create:
auto`; the side-effect rule would also have to yield to it.

## Comparison

| Criterion | A | B | C | D |
|---|---|---|---|---|
| "Remind me tomorrow" sets it at once | yes | yes | no | yes |
| "Remind me every Monday" sets it at once | yes | no | no | yes |
| Tasks keep their confirmation | yes | yes | yes | no: the override is per tool |
| An injected page can set a reminder without a press | no | no | no | yes |
| A mistake is undone in one press | yes, Delete | yes | n/a | no button |

## Decision

**A.** A reminder only writes to the owner, in the owner's own chat, at a
time the owner asked for, and costs nothing to run; the press guards
nothing a Delete button does not undo at once. A recurring reminder is
the same in kind, so B keeps a press with no reason the owner can see.
D cannot tell a reminder from a task, drops the taint check and needs
the config wiring first.

The rule:

- `schedule.create` with `kind: reminder` in a session that is not
  tainted: auto. A reminder cannot name another conversation (it has no
  grant), so it always goes where it was asked.
- In a tainted session the existing side-effect rule asks, so an
  injected page still cannot plant one.
- A task, one-time or recurring, asks as before (R-POL-8).
- Arguments that do not parse fall through to the side-effect rule and
  ask, as every rule here fails closed.
- The created reminder, with or without a press, is shown as its own
  message with Delete (the quick action of a listed job). The tool's
  result tells the model the owner already sees it, so the model only
  confirms in a few words.

## Security

- **Trust boundary:** the policy for a side effect in a clean session,
  which was "the owner presses" and becomes "the owner's own message".
- **What an attacker controls:** text that enters the session (a page, a
  forwarded message) taints it, and the rule then asks; a clean session
  holds only the owner's words and the node's own results.
- **Abuse cases:** a page asking for a reminder that tells the owner to
  do something: the session is tainted, so it asks. A model that misreads
  the owner sets a wrong reminder: it is shown at once with Delete. A
  flood of reminders: the model's calls per turn are limited (R-AGT-4),
  each is shown, and `schedule.list` lists them with Delete.

## Consequences

- The plain request takes one message and no press; the owner sees what
  was set and can undo it.
- The confirmed path (a tainted session) also shows the reminder's
  message with Delete after the press.
- A new policy rule with tests, one message text in every language of
  `texts`, the tool's description and the guide.

## Revisit when

A reminder the owner did not ask for is set in a clean session, or tasks
turn out to be as harmless to set.
