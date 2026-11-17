# ADR-0070: Bound the agent's fix-and-rebuild loop by builds in a row, not by turn depth

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-17), [7.2](../spec.md#72-conversations-history-and-the-agent-loop) (R-LIVE-9), [7.6](../spec.md#76-action-policies-and-risk-classes); [M4c design](../design/p1-m4c-reflexes-design.md) section 10; refines [ADR-0069](0069-background-jobs-with-a-live-card.md)

## Context

ADR-0069 moved `module.build` into the background: the build's end
resumes the conversation with a new turn, as a host command's end does.
That path continued at the call's depth plus one, and the node's policy
refuses every call deeper than 3: the bound that stops an agent from
starting itself without end. With write → build → fix → build, each
build spent one level, so after about one fix the agent could call
nothing, `module.install` included, until the owner wrote again. Before
the move the same loop ran inside one turn, bounded by its step limit.

## Options

### A. Keep depth + 1

The loop allows about one fix; the skill tells the agent to hand back to
the owner.

### B. Resume at the call's own depth, with no other bound

A build is the node's work, not the agent calling itself. But a model
that keeps failing could build, fix and build without end, spending the
owner's limits.

### C. Resume at the call's own depth; at most N builds of a module in a row without the owner

The 6th `module.build` of a module in a conversation without a message
from the owner is refused in the node's words; the owner's message resets
the count.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Fixes the agent can make alone | ~1 | Unbounded | 5 builds |
| A failing model stops by itself | Yes, early | Only at the daily limits | Yes, after 5 |
| The owner is asked | After the 2nd failure | Never | After 5 builds |
| Effort | None | S | S |

## Decision

**C with N = 5** (owner, 2026-11-10). The count is kept in memory per
conversation and module; a restart resets it, and only a person's message
in the conversation resets it otherwise (a build's own turn, a press or a
scheduled run does not).

## Security

- **Boundary:** the bound on the agent's self-started work. Depth still
  bounds every other self-resume (host commands, presses).
- **What an attacker controls:** nothing new; `module.write` already
  refuses a tainted session (ADR-0067), so outside text cannot drive the
  loop.
- **Abuse cases:** a model that loops on builds stops after 5 per module.
  A model that builds under new module names gets a fresh count per name;
  the daily turn and token limits bound that, and every new folder is a
  workspace commit the owner sees.

## Consequences

- The agent fixes its own compile errors several times before asking.
- A scheduled task that builds cannot reset its count (no person writes
  there) and is stopped after 5 builds until a restart.
- The count is not durable: a restart allows 5 more.

## Revisit when

Builds move to a worker (P2), or the per-name loophole shows up in use.
