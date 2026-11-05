# ADR-0047: During development, Sonnet answers and Haiku does compaction and checks

- **Status:** Accepted
- **Date:** 2026-11-04
- **Deciders:** Igor Artasevych
- **Spec:** [7.3](../spec.md#73-model-router), [7.4](../spec.md#74-providers-and-model-access), [11](../spec.md#11-open-decisions) ("First API provider", "Choosing models by purpose")
- **Supersedes:** [ADR-0022](0022-haiku-first-during-development.md)

## Context

ADR-0022 put Haiku first and DeepSeek as the fallback, with Sonnet left out of
the catalogue and unmeasured. Three things changed:

- The owner, 2026-11-04: the lower model of a provider is what agents use for
  filtering and checks, not as their main model, and a smarter model makes
  fewer childish mistakes.
- The owner decided that base mechanisms are developed on the main model only.
  OpenRouter fallbacks are a separate topic for later.
- A silent switch between models hides which model behaved how, and chatlab
  ([design](../plans/p1-m3c-chatlab-design.md)) is about to measure behaviour.

[The comparison](../research/2026-11-04-sonnet-or-haiku.md) ran the same 20
turns as ADR-0022 through Haiku and Sonnet. Both answered 20 of 20, and the
differences are the kind the owner names: Haiku named a film it then took back,
and removed more than it was asked to from a list.

The router needs no change. A turn prefers the `smart` tier, compaction and the
memory check prefer `cheap`, and the other tier is the fallback.

## Options

### A. Sonnet in `smart`, Haiku in `cheap`

Turns go to Sonnet, compaction and the memory check to Haiku. Haiku also takes a
turn when Sonnet is cooling down after its limits.

### B. Sonnet for everything

One model in the catalogue. Nothing can silently answer in its place, but a
Sonnet out of limits stops the bot, and it also stops the memory check, which
fails closed.

### C. Opus in `smart`

The owner would rather develop on Opus. Today's turns are conversation and tool
calls, which give it little to do. It is slower and takes more of the limits.
It is not measured here.

### D. Keep Haiku first

Should take the least of the limits, and is the faster of the two. It keeps the
kind of slip above.

## Comparison

| Criterion | A. Sonnet + Haiku | B. Sonnet only | C. Opus | D. Haiku |
|---|---|---|---|---|
| Median latency of a plain turn | 6.0 s | 6.0 s | Not measured | 4.2 s |
| Slips in the 20 turns | None found | None found | Not measured | Several |
| Out of Sonnet's limits | Haiku answers | The bot stops | — | — |
| Memory check | Haiku, as measured | Sonnet, not measured | Haiku | Haiku, as measured |
| Use of the subscription's limits (expected, not measured) | More than D | More than A | Most | Least |

## Decision

**A**, for the development period. Opus comes in when the agent writes skills or
modules, which is a task that needs it.

The claude CLI is the only route. A scheduled agent turn is a turn, so it runs
on Sonnet. That corrects the spec's "cron → cheap": the owner's aim was never a
cheaper model for regular work, but a regular task that needs no model at all,
which becomes a reflex (spec section 1). A task that still needs a model is what
has not become a reflex yet.

## Security

- **Trust boundaries:** none new. Both models go through the same CLI provider
  and MCP bridge (ADR-0036).
- **What an attacker controls:** a web page in a fetch, so what the model reads.
- **Abuse cases:**
  - An injection on a page: the policies and the journal hold whichever model
    answers. The acceptance's injection scenario passes on Sonnet.
  - A weaker memory check: the check keeps its model. When Haiku is out, it falls
    to Sonnet, which the acceptance has not measured on the case table.
    `guardtest`'s table has to pass before the check's model is changed on
    purpose; that rule goes into the ADR for the decisions role.

## Consequences

- `models.example.yaml` lists Sonnet and Haiku, and `make accept` runs this
  catalogue, on the CLI route by default. It passes on Sonnet: every turn in the
  transcript was Sonnet's. It took 240 s, against 168 s for Haiku in the previous
  run.
- Chatlab counts a turn Haiku answered in Sonnet's place as inconclusive.
- These runs take from the same subscription as the owner's own work. What a
  turn costs in limits is not measured; chatlab's report shows tokens.
- A fix that only works on Sonnet can hide a weak rule. Chatlab's language
  baseline runs on both models to show which it is.
- `models.local.yaml` is the owner's file and does not change by itself.
- On acceptance, ADR-0022 is marked as superseded by this one.

## Revisit when

- The agent writes skills or modules: Opus.
- The subscription's limits bite.
- The OpenRouter fallbacks are taken up, or the bot serves more people than the
  owner.
