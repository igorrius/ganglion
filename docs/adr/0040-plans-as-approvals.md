# ADR-0040: Let one press on a plan approve exactly the commands it lists

- **Status:** Accepted (2026-11-09)
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.12](../spec.md#712-host-commands-and-plans) (R-CMD-6, 7), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-2, 4, 6, 8); [M3d design](../design/p1-m3d-host-commands-design.md) section 7; [ADR-0032](0032-buttons-and-resume.md) (buttons and resume), [ADR-0038](0038-host-commands-as-argv-with-classes-in-code.md)

## Context

Commands that change something need the owner's press (ADR-0038). A
cleanup takes several of them; a press per command is tiring, and the
owner's view is that once they asked for it ("clean the Docker images"),
asking again is pointless.

But code cannot check that a command matches the owner's words. In the
OpenClaw transcript "remove the images too" became
`docker rm $(docker ps -aq)`, and the assistant believed it was doing as
asked. In a tainted session a page can lead the model to "remember" a
request the owner never made.

The owner liked one more thing there: the assistant posts a plan with
checkboxes before the work and ticks them as it goes, so it plans first
instead of charging in.

Scheduled tasks already carry a grant approved once at creation
(R-POL-8): tools, hosts and the reply target. The owner wants them to run
approved commands unattended too, e.g. a weekly Docker image prune.

## Options

### A. A press per command (today's confirmation)

Every change command is its own question.

### B. A plan with one press for its exact commands

The agent posts a plan message: steps in plain words; each step that
needs a press shows its exact argv, the table's fixed consequence line
and the agent's one-line reason. One "Run" approves exactly those argv
in this conversation until the plan ends or expires. A plan with no such
step runs without a button. A change outside the plan does not run.
Task grants list exact argv the same way.

### C. A plan with patterns

As B, but a step may approve a pattern ("`docker rm` of any stopped
container", "`rm` under `~/.cache`").

### D. The model's reading of the owner's words

The model marks a call as "the owner asked for this"; code trusts it.

## Comparison

| Criterion | A. Per command | B. Exact plan | C. Patterns | D. Model's word |
|---|---|---|---|---|
| Presses for a three-step cleanup | Three | One | One | None |
| The 19-containers case | Asked separately | Not in the plan → not run, own press | Runs if the pattern was broad | Runs |
| Survives a tainted session | Yes | Yes: the owner sees every command before the press | Partly: a pattern hides what will run | No |
| Owner sees the impact | Per question | Fixed lines for every step at once | For the pattern only | No |
| Steps that depend on what an earlier step finds | Natural | A new plan after looking | Natural | Natural |
| Effort | Exists | Medium | Medium, plus pattern semantics | Small |

## Decision

**B.** It is the owner's "I already said so", made into one press on
something code can check: the exact commands. It keeps the plan the owner
liked and turns it into the boundary, so the agent cannot do more than it
showed. Patterns (C) bring back the gap between what was shown and what
runs; trusting the model (D) is the OpenClaw failure.

The plan goes through the existing pending-call machinery of ADR-0032: a
random one-time button id, only the addressee presses, a double press runs
once, no answer within the TTL denies (R-POL-2), the press resumes the
turn. The plan message stays in the chat with its outcome as the record
of what was done.

## Consequences

- New tools `plan.show` and `plan.step` (names are the builder's); the
  transport only edits a message and knows nothing about plans.
- A step whose arguments depend on an earlier step's output (container
  ids) needs a second plan after the agent has looked.
- Task grants (R-POL-8) gain exact commands with their consequence lines;
  a run needing anything else is denied with one notice.
- The plan grant covers host commands only in M3d; plans that approve
  other tools' calls are in "Not now".
- Asking the model to plan is a prompt directive; code enforces only that
  nothing which needs a press runs outside an approved plan or its own
  confirmation.

## Revisit when

- Owners keep needing a second plan for dependent steps (then consider
  narrow, typed patterns for specific commands, e.g. "these container ids
  from the listing just shown").
- Other tools join plans.

## Refined by the build (2026-11-08)

The plan for tasks 3.21–3.27 ([p1-m3d-commands.md](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-m3d-commands.md))
settled these:

- **The approved plan's question becomes the plan's message.** It is not
  folded (R-POL-9 already says a plan does not fold). The edit after the
  press replaces the buttons with the ticked steps. A declined or expired
  plan folds like any question.
- **`Call.Planned`** reports whether the conversation's approved plan
  lists the command, argv and folder. The agent fills it from the plan
  store, never the model.
- **A command without a folder runs in home, and matches there.** Every
  approval and grant is matched with `tool.Command.SameAs`: argv exact, and
  the folders equal once "" is read as the node user's home. A plan step
  without `dir` approves `host.exec` with `dir` set to home, and the
  reverse; any other folder stays another command. The plan's message
  shows the folder as the question does, home for a step without one.
- **`DecisionPlanned`** is a new decision in the journal, for a call that
  ran under an approved plan.
- **What runs at once.** A plan with no step that needs a press runs at
  once and keeps the approved commands it still lists. A plan that lists
  approved commands only also runs at once. Any other command that needs a
  press asks.
- **`Spec.KeepsQuestion`** marks a tool whose question stays in the chat
  as the record, so the agent does not fold it.
