# ADR-0069: Run long node work as background jobs with a live card the node edits

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.2](../spec.md#72-conversations-history-and-the-agent-loop) (R-LIVE-9), [7.12](../spec.md#712-host-commands-and-plans) (R-CMD-4), [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-9, R-RUN-17), [7.9](../spec.md#79-observability) (R-OBS-11); [M4c design](../design/p1-m4c-reflexes-design.md) section 10; [ADR-0051](0051-status-reactions-outside-the-outbox.md), [ADR-0052](0052-a-plan-is-delivered-during-its-turn.md)

## Context

M4c's live check: the agent wrote a module and called `module.build`
inside a turn. With the `claude` CLI route one model call runs all of a
turn's tool calls, and it is limited to 60 s (turn 2 min, handler 3 min).
The build was stopped after 10 s, mislabelled as its own 10 min limit,
and the turn failed. A real build takes a minute or more.

Host commands already go to the background after 20 s, but then nothing
shows that they run: reactions and typing end with the turn, the model is
not told in later turns, `/status` does not list them, and the result
arrives up to 30 s late. The owner named this as the general pain of
agents: no sign of life (owner, 2026-11-10).

## Options

### A. Raise the limits so a build fits in a turn

Calls and turns of 12–15 min, the lease above them.

### B. Background build only

`module.build` returns at once; a completion event resumes the
conversation, as `host.exec` does today. No card.

### C. Background jobs with a live card, one mechanism for builds and host commands

A node-side job registry; a card message the node edits on a timer; an
immediate completion event; a node note in every turn listing the
conversation's running jobs; a `/status` section.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| A build of several minutes | Holds a turn and a worker for minutes | Works | Works |
| The person sees it is alive | Typing only | No | A ticking card |
| The model knows what runs | Yes, it waits | Only from old history | A note every turn |
| Chat usable meanwhile | No, the turn holds it | Yes | Yes |
| Host commands get the same | No | No | Yes |
| Effort | S | M | M–L |

## Decision

**C** (owner, 2026-11-10), with A's smaller part: a model call gets 3 min
and a turn 5 min, set in `ganglion.yaml`, because writing a module's code
is a long answer on the CLI route.

- A job's row lives in the node's database; a restart ends a running job
  as interrupted, and its card and its completion say so.
- The card is the node's message with fixed texts, edited through the
  outbox (`EditOf`), every 10 s, sent mid-turn or outside any turn like
  the plan message (an exception to R-DLV-4 that ADR-0052 already
  makes).
- One build runs at a time; others queue. The same module cannot be
  built or written while its build runs or waits.
- The chat stays alive: new messages start ordinary turns (owner).

## Security

- **Boundary:** none new. The card carries only the node's words, the
  job's title (a module name or the approved command's argv, both already
  shown to the owner) and counts; never build output or command output.
- **What an attacker controls:** a build's output (dependencies) and a
  command's output. Neither reaches the card. The completion note follows
  the existing rules: a command's output is outside content (R-CMD-1); a
  build's answer is clean only under ADR-0067/0068's provenance rule.
- **Flooding:** one card per job, edits every 10 s and only while it runs;
  one build at a time.

## Consequences

- The person always sees what the node does in the background and that it
  is alive; the agent answers "how is it going" from facts.
- The node gains a job registry, a ticker and a card renderer; `host`
  moves its announce onto it.
- A frozen card is the visible sign of a dead node; a restart turns it into
  "interrupted".

## Revisit when

Jobs move to workers over NATS (P2), or Telegram edit limits bite in group
chats.
