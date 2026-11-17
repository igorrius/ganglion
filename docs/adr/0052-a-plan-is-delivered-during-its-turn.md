# ADR-0052: Deliver a plan during its turn through the outbox, with its own table

- **Status:** Accepted
- **Date:** 2026-11-05
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-4, R-DLV-5), [7.2](../spec.md#72-conversations-history-and-the-agent-loop) (R-LIVE-8), [7.12](../spec.md#712-host-commands-and-plans) (R-CMD-7); [live turns design](../design/p1-live-turns-design.md), section 5; [ADR-0040](0040-plans-as-approvals.md)

## Context

The owner wants to see what is happening with a task, and liked a plan with ticks. The M3d
design (7.1) holds that message; [ADR-0040](0040-plans-as-approvals.md) adds a press to it.
This milestone builds the display alone, ahead of M3d.

A tool's messages today go out at `Complete`, with the turn's reply (R-DLV-4). A plan sent that
way arrives with the answer, ticked to the end, which is worth nothing. Also, a press starts a
new event with a new turn id, so a plan that M3d parks must outlive its turn, and state written
at `Complete` comes too late.

## Options

### A. `Outbox.Enqueue` during the turn, and a table of its own

The plan's first message and its edits enter the outbox at once, under keys from the plan. One
active plan per conversation lives in a `plans` table, written as it is shown. The sender
delivers them with its usual retries.

### B. Delivery at `Complete`

As any tool's messages are. Nothing new, and no exception to R-DLV-4.

### C. A direct send from the tool

The tool calls the transport and keeps the message id in session state.

## Comparison

| Criterion | A. Enqueue | B. At `Complete` | C. Direct |
|---|---|---|---|
| Seen while the work goes | Yes | **No** | Yes |
| Retries, `429`, order with other messages | The sender's | The sender's | Its own, a second path |
| A rerun after a crash | The same key, no second message | Nothing was sent | A second message |
| Survives a press's new turn | Yes | Only through state | Only through state |
| An exception to R-DLV-4 | Yes, stated | No | Yes, wider |

## Decision

**A.** B does not do what was asked. C builds a second path to Telegram around the guarantees
that the outbox already holds.

**The exception to R-DLV-4 is small.** A plan is progress, not the reply. It can reach the chat
from a turn that then fails or crashes, and the rerun finds it and goes on editing, so the
message says what is known. A superseded answer takes the same way (ADR-0050).

- **Tools.** `plan.show` and `plan.step`: `ClassRead`, `PolicyAuto`, `AdminOnly`, not
  `LocalSafe`: a local-only session has no plan, since the message leaves the node.
- **The table.** `plans`: `conv` as primary key, `idem_key`, `steps` as JSON, `version`,
  `active`, `dirty`, `last_edit_ms`, `updated_ms`, written in its own transaction when shown.
  The row, not the call key, finds the message, so a rerun's or an addition's second `plan.show`
  changes the same message. An active plan untouched for 1 hour counts as closed (the M3d
  design's expiry).
- **Keys.** The first message is `plan:<call key>:show`, and an edit is
  `plan:<idem>:v<version>`.
- **Edits** are at most one per 2 s; between them the plan stays dirty and the next step or the
  end flushes it. This is for Telegram's limits and against flicker.
- **The budget.** On the CLI route a turn may make `MaxSteps` tool calls, so ticking would eat
  them. Plan calls do not count against that cap and have their own ceiling of 32 per turn. The
  prompt asks for `plan.step` beside the next real call, so a tick costs no round trip.
- **The end.** `finish` calls `Plans.Finish`, with `kept` when the turn parked a call. If kept,
  the lines stay as they are, for the press. If not, ⏳ and · become ⏭ and the plan closes.
- **Limits.** 1 to 20 steps, one line of at most 200 runes each, a note of at most 200, and at
  most 3,500 UTF-16 units rendered, so it always fits one message. A call past them returns an
  error to the model.

## Security

- **Trust boundaries.** The model's output reaches the chat mid-turn, a little earlier than a
  reply does, and from a session that may be tainted.
- **What an attacker controls.** A page the model read can steer the text of a plan.
- **Abuse cases, all stopped by code:**
  - **Plan text carries an injection into the chat.** It is model text, rendered as a reply is:
    entities, never markup the model controls (ADR-0025). The plan has no button.
  - **A plan reads as an approval before M3d.** No button, and no tool checks a plan. It displays;
    policies decide per call. A test asserts that no button appears.
  - **A flood of plan calls.** The ceiling of 32, the limits on size and the 2 s throttle.
  - **A plan writes elsewhere.** The conversation comes from the call, not from the arguments, so
    a plan goes only to the conversation that holds the turn.
  - **A non-admin's session, or a local-only one.** Not offered.
  - **A taint escape through the message.** Text leaves the node to the person in that chat, as
    a reply would. Nothing else reads it. In a tainted session, a page can make a plan say
    something false; a reply could too.

## Consequences

- The plan is seen as work goes on, and survives until M3d's press needs it.
- M3d's task 3.24 keeps only the press; ADR-0040's approval is unchanged.
- A chat can hold a plan from a turn that later failed. `Finish` marks its open lines ⏭, except
  after `kill -9`, where the rerun continues it.
- One more table, and a tool that writes outside the turn's transaction.

## Revisit when

- Telegram's limits or the owner's eye say that 2 s is wrong.
- API-route tools run in parallel: each line already changes by itself.
- Plans should run on a local-only session, through a transport on the node.
