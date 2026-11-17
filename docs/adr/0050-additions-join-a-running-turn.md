# ADR-0050: Let a message written during a turn join it at the next boundary, ending a CLI run to do so

- **Status:** Accepted
- **Date:** 2026-11-05
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-4, R-DLV-6), [7.2](../spec.md#72-conversations-history-and-the-agent-loop) (R-AGT-1, R-LIVE-1..6); [live turns design](../design/p1-live-turns-design.md), section 3

## Context

The owner sends a task and, while the agent works on it, writes more. They mean one
thought, widened. Today each message is its own turn, in order (R-AGT-1): the first runs
to its end, its answer may be out of date, and the second spends another model call.

A model hears nothing while it writes, so an addition can enter only between calls. On
the API route `agent.steps` holds those boundaries. The route to Sonnet is the claude
CLI, whose tool loop runs inside the CLI, and every tool call passes through the core
(ADR-0036). So the CLI route needs its own answer.

The owner set a bar: ending a run stays if a turn with an addition costs at most twice
what two separate turns cost together. It was
[measured](../research/2026-11-05-cost-of-an-addition.md) before anything was built, with
Sonnet and the node's prompt and flags, 3 repeats each:

| | Two separate turns | With an addition | Ratio |
|---|---|---|---|
| Cost, USD, mean | 0.0287 | 0.0303 | **1.06** |
| Input tokens, all kinds | 6,497 | 6,746 | 1.04 |
| Wall time, s | 11.5 | 13.0 | 1.13 |

The ratio is close to 1 because neither case reuses the history: only the system prompt
stays in the cache across runs.

The design also rejected a quiet window before a turn starts, by the owner's word: their
additions come well after the first message.

## Options

### A. End the CLI run at a tool boundary, and start a new one

When a tool call returns and an addition waits, the core cancels the run with a cause of
its own and starts a new one with the whole history and the addition. The core already
sends the whole history on each call and logs the calls a run made (`takeExecLog`), so
both routes run the same loop.

### B. Write the addition into the running CLI's stdin

`--input-format stream-json` keeps the process open and takes more messages.

Measured without tools: a message written during a model call did not join it. The call
ran to its end, and the CLI took the message 0.6 to 1.9 s after, as a new call. That call
read the earlier request from the cache and cost about half of A's second run. Whether a
message reaches the model between two tool calls of one run was not tried, and the
[headless docs](https://code.claude.com/docs/en/headless) do not promise it.

### C. Append the addition to a tool's result

The next model call inside the run would see it, with no new run.

## Comparison

| Criterion | A. End the run | B. Stdin | C. In a tool's result |
|---|---|---|---|
| Stops the next tool call | Yes: the core refuses it | Not shown | Only for later calls |
| The history's owner (ADR-0020) | The core | The CLI's session too, so two copies | The core |
| Cost against two separate turns | 1.06 | Cheaper, as the cache holds | About the same as one call |
| Rests on | Our own code | Behaviour nobody promised | Our own code |
| An owner's words arrive as | A user message | A user message | **Data marked as outside content** (ADR-0031) |
| Both routes run the same loop | Yes | No | No |
| Text the run wrote so far | Lost | Kept | Kept |

## Decision

**A.** It is within the bar by a wide margin. It tells the model the plain truth about who
said what, it can stop the next tool call, and it rests on nothing unpromised. B is
cheaper but does not show the one thing an addition needs. C is out on security alone:
outside text must never look like an owner's words, and an owner's words must not arrive
as data.

What goes with it:

- **The taking rule** is R-LIVE-1: an unbroken run behind the turn's event, same person,
  not a command, not expired, within limits. It stops at the first that does not qualify,
  so "also check logs" followed by `/new` joins the first and leaves `/new` to archive a
  session with the finished answer in it.
- **The queue.** `queue.Queue` gains `Behind`, a read of what waits behind an event, and
  `StateUpdate.Taken`, which `Complete` marks done with `taken_by` **in its one
  transaction** (R-DLV-4). A taken id from another conversation changes nothing. After a
  crash before `Complete`, nothing was marked, and the turn takes them again.
- **An ended run** has its own result, never recorded: not a failure, not a fallback and
  not a retry. Its cancel cause tells it from a timeout.
- **Its tokens.** With `--output-format json`, usage arrives only in the final result, so
  an ended run reports none. The core adds the estimate it uses for compaction (runes / 4)
  of what it sent, once for each model call the run made: its tool calls plus one. This
  leaves out what the model wrote and the tool results it read, and it counts each call
  in full, so it can be off either way. The daily limit is a guard against runaway use and
  not a bill, and the alternative, moving the main route to `stream-json`, is a larger
  change.
- **The text an ended run wrote is lost.** Only its tool calls and results survive, since
  `--output-format json` gives text only in the final result. The next call has every
  result. On the API route such text goes out as a quote (R-LIVE-5).
- **The earlier answer** goes out through `Outbox.Enqueue` as soon as an addition is seen,
  under `<idem>:earlier:<n>`, with `Outgoing.Superseded`. It stays in the history as
  written, since the person saw it. Each transport decides how to show it.
- **Time.** No addition is taken with under 30 s left on the turn's context.

**A rerun's call numbers.** A skipped or refused call has no number and no journal row. But
a rerun after a crash may take additions at other moments, so `c.Key()` (`turn:N`) may name
a different call than it did before the crash. No change is needed: where a key guards a
side effect, a key that returns is refused or deduplicated, and nothing guarded runs twice:

| Where the key is used | A different call under a key that returns |
|---|---|
| `pending.Ask` | Returns the stored id. The press runs what the question showed, the stored arguments, and not the new call |
| `skills.Runs.Begin` | Refused: begun before |
| Outbox keys (`message.send`) | The second message is dropped |
| Cells (`SetDesired`) | Not guarded: the key is only the change's source. It sets the value, and a switch set twice holds one value |
| Journal | The later row replaces the earlier one |

The cost is narrow: after a crash, a second call at that number may not take effect while
the model believes it did. The same is true today, since a rerun calls the model again and
a model need not repeat itself. Additions make it more likely but do not make it new.

## Security

- **Trust boundaries.** No new way in: additions are inbox events, past the access list as
  any message is. The new read is local SQLite.
- **What an attacker controls.** A forward's or a quoted reply's text, a page a tool read,
  and a person in a group.
- **Abuse cases, all stopped by code:**
  - **A page's injection poses as an addition.** Additions come only from inbox events.
    Tool results never become additions.
  - **A forward arrives mid-turn, hoping to run with a clean session's rights.** It goes
    through `userContent` and sets taint before the next model call. Its web addresses do
    not become known, as at a turn's start. No call starts while it waits, so nothing runs
    on a stale clean flag. A tool already running keeps the decision it had, which was
    right for what was known.
  - **Someone in a group adds to the owner's turn, borrowing the admin's rights.** Only the
    same principal qualifies, and the run stops at anyone else.
  - **A flood keeps the turn alive and burns tokens.** `MaxSteps`, the turn's clock and the
    daily limit cap it; the check for limits sends no alert, as that belongs to the message's
    own turn. The rest become turns, as today.
  - **A stream of additions keeps tools from running.** Only the same person can do this,
    and only until `MaxSteps`.
  - **An addition slips past a parked call.** A press runs exactly the stored call once. An
    addition grants nothing and cancels nothing.
  - **A local-only session.** An addition does not change it.

## Consequences

- A person can widen a request, and one answer takes it all in.
- Both routes share one loop, so a rule added there holds for both.
- `agent.steps` grows, and the queue's interface gains a method every fake must have.
- A killed CLI process may be billed for a call it never returned. Task 6's scenario
  compares a real turn with these numbers.
- Tools may be held back while a person keeps writing, by design.

## Revisit when

- A real turn in `addition-real` costs over the bar.
- The main route moves to `stream-json` for another reason: B's cost and A's lost text both
  change then.
- The CLI promises when a message written to stdin is picked up.
