# ADR-0022: During development, Haiku through the claude CLI answers first and DeepSeek V4.1 Flash is the fallback

- **Status:** Superseded by [ADR-0047](0047-sonnet-answers-during-development.md)
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.3](../spec.md#73-model-router), [7.4](../spec.md#74-providers-and-model-access), [11](../spec.md#11-open-decisions) ("First API provider"); P1 decision D-6

## Context

M3a made the agent talk through models the operator lists in a catalogue
(ADR-0021). D-6 asks which model answers first.

[The comparison](../research/2026-11-02-first-model.md) ran 20 scripted
turns through three models:

| Model | Answered | Median latency | Cost per turn | Quality |
|---|---|---|---|---|
| Haiku through the claude CLI | 20/20 | 3.5 s | $0 (subscription) | Good |
| DeepSeek V4.1 Flash (OpenRouter) | 20/20 | 2.3 s | $0.00015 | Good |
| Qwen 3.8 Flash (OpenRouter) | 20/20 | 6.1 s | $0.00015 | Weaker |

A node is one operator, one agent and one bot for now (spec 14). Which
smarter models to use, and for what (conversation, writing tools,
analysis), is not settled yet.

## Options

### A. Haiku first, DeepSeek as the fallback

Haiku costs nothing within the owner's subscription and knows the date,
because the CLI adds it. DeepSeek takes over when the subscription is
limited or the CLI fails. It also handles tasks that need tools until the
CLI provider supports them (M3b).

### B. DeepSeek first, Haiku as the fallback

DeepSeek is faster and does not depend on subscription limits. It costs
about $1–2 a month at 300 turns a day.

### C. Haiku first, Sonnet for people's messages

Sonnet would be in the smart tier, so it would answer people's messages
first. It is slower and uses more of the subscription.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Cost | $0 plus rare fallbacks | ~$1–2 a month | $0, but more of the subscription's limits |
| Latency | 3.5 s | 2.3 s | Higher |
| Quality on the test set | Good | Good | Not measured |
| Depends on the subscription | Yes, with a fallback | No | Yes |

## Decision

**A**, for the development period: the owner is the only user, and the
subscription covers the traffic. Both models go in the `cheap` tier, so
the router tries Haiku first (price 0) for messages and compaction, then
DeepSeek. Sonnet stays out of the catalogue.

## Consequences

- `models.example.yaml` lists the same pair, with the OpenRouter part
  commented out.
- Choosing smarter models by purpose becomes an open decision in spec
  section 11. The router's tiers and the skills' `model:` frontmatter
  (spec 7.3) are where that choice will live.
- The first API provider of spec section 11 is decided for now:
  OpenRouter, as the fallback.

## Revisit when

- The bot serves more people than the owner.
- The subscription's limits bite.
- A task needs a stronger model: writing tools, analysis.
- The decision on choosing models by purpose is taken.
