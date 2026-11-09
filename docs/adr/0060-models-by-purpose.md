# ADR-0060: Choose models by purpose, each with an explicit order and an optional effort

- **Status:** Proposed
- **Date:** 2026-11-09
- **Deciders:** Igor Artasevych
- **Spec:** [7.3](../spec.md#73-model-router) (R-RTR-1, R-RTR-6), [7.4](../spec.md#74-providers-and-model-access), [11](../spec.md#11-open-decisions) ("Choosing models by purpose"), [14](../spec.md#14-not-now) ("Models by purpose"); [M4 design](../plans/p1-m4-reflexes-design.md) section 5; [ADR-0034](0034-clean-context-guard.md), [ADR-0047](0047-sonnet-answers-during-development.md), [ADR-0059](0059-node-config-file.md)

## Context

The router today reads a catalogue where each model carries a tier:
- `smart` or `cheap`;
- a person's message and a scheduled agent turn try `smart` first;
- compaction and the clean-context check (ADR-0034) try `cheap` first;
- the other tier is the fallback, and within a tier the cheaper model
  goes first.

ADR-0047 is expressed in these terms: Sonnet in `smart`, Haiku in
`cheap`. What the tiers cannot say:
- **The order.** It comes from prices, and subscription models cost 0, so
  two of them tie and the file order decides by accident.
- **Keeping a model out of a person's conversation.** A model good enough
  for yes/no checks lands in the fallback chain of every turn as soon as
  it is in a tier. The spec's "Not now" asked for exactly that separation.
- **Writing code.** ADR-0047 says Opus comes in when the agent writes
  modules or skills; no tier fits it, and P3 needs it.
- **Reasoning effort.** OpenAI-compatible APIs take `reasoning_effort`;
  the catalogue has no field for it.

The spec's open decision "Choosing models by purpose" was to be decided
when the agent starts writing skills or modules. M4 moves the catalogue
into `ganglion.yaml` (ADR-0059), so its shape is decided now.

## Options

### A. Keep the tiers

`smart` and `cheap`, cheaper first within a tier. Opus would be a third
tier or a skill's special case.

### B. Tiers plus a `code` section

The two tiers stay for chat, compaction and checks; a separate list names
the models that write code.

### C. Purposes, each an ordered list of `{model, effort}`

The models are listed once. A fixed set of purposes in code (`chat`,
`check`, `compact`, `code`) each has an ordered list of entries, every
entry a model and an optional effort. Every place that calls a model
names its purpose.

## Comparison

| Criterion | A. Tiers | B. Tiers + `code` | C. Purposes |
|---|---|---|---|
| Order of candidates | By price; ties by accident | Same for three uses | As written |
| A check-only model stays out of turns | No | No | Yes: it is listed only under `check` |
| A model for writing code | No place | Yes | Yes |
| Effort per use | No | Only for `code` | Per entry |
| A skill asks for a kind of model | `tier` | `tier`, or a special word | `purpose` |
| Size of the config | Smallest | Small | Larger: each purpose is listed |
| Router code | Ranking by price and tier | Ranking plus a list | A list walk; no ranking |

## Decision

**C** (owner, 2026-11-09). The order the operator writes is the order the
node uses, and each use gets only the models meant for it.

```yaml
models:
  providers:
    - {name: claude, type: claude-cli}
  models:
    - {id: sonnet, provider: claude, name: sonnet, tools: true, context: 200000}
    - {id: haiku,  provider: claude, name: haiku,  tools: true, context: 200000}
    - {id: opus,   provider: claude, name: opus,   tools: true, context: 200000}
  purposes:
    chat:    [{model: sonnet, effort: medium}, {model: haiku}]
    check:   [{model: haiku, effort: low}, {model: sonnet, effort: low}]
    compact: [{model: haiku}]
    code:    [{model: opus, effort: high}, {model: sonnet, effort: high}]
```

- **The purposes are fixed in code:** `chat` (turns and scheduled agent
  tasks), `check` (short yes/no decisions such as the clean-context
  check), `compact` (history compaction), `translate` (fixed texts into a
  language with no file, R-LANG-5; found by the M4a plan), `code`
  (writing modules and skills). An unknown purpose fails the start.
  `check`, `compact` and `translate` use `chat`'s list when absent.
- **The budget:** over the daily budget, a turn (a person's or a
  scheduled agent turn) keeps free and local models only, whatever
  purpose a skill moved it to; the calls that are not a turn's own steps
  (compaction, the clean-context check, translation) are housekeeping and
  are not cut. A skill may not ask for `code` until P3.
- **The order is the preference.** The next entry is taken when one is out
  of limits, answers 429 or times out; cooling and limits work as today.
- **`check` keeps a fallback:** ADR-0047's clean-context check fell back
  to Sonnet when Haiku was out of limits, and the example keeps that. A
  purpose whose list is used up fails closed, as today.
- **`effort` is optional.** It is passed to providers that take one
  (`reasoning_effort` on OpenAI-compatible APIs). A provider that cannot
  take an effort set in the file fails the start. The claude CLI takes
  `--effort` with `low`, `medium`, `high`, `xhigh`, `max` (checked on
  2.1.319); OpenAI-compatible APIs take `low`, `medium`, `high`.
- **`code` is checked but unused until P3.** Without it, P3 answers "no
  model to write code" instead of picking one.
- **A skill's frontmatter `tier` becomes `purpose`.**
- The spec's open decision closes for these roles. Embeddings and vision
  stay in "Not now"; a classifier (router layer 4) stays in P2.

ADR-0047's choice stands: Sonnet answers, Haiku compacts and checks. It is
now written as purposes instead of tiers. ADR-0047 is accepted and not
rewritten; this ADR changes how its practice is expressed, and its
"Revisit when: Opus" is the `code` purpose.

## Security

- **Trust boundaries:** none new. Every model still goes through its
  provider and the same policies.
- **What an attacker controls:** the content a model reads (a page, a
  message), and through it what a weaker model may be talked into.
- **Abuse cases and what stops them:**
  - *A model chosen for cheap checks answers a person and is easier to
    inject.* It answers only if the operator lists it under `chat`; the
    router takes candidates from the purpose's list alone.
  - *The clean-context check runs on a model nobody measured.* It runs
    only on the `check` list; when the list is exhausted the check fails
    closed (ADR-0034). `guardtest`'s table still has to pass before the
    check's model is changed on purpose (ADR-0047).
  - *An effort the operator set is silently dropped.* The start fails.

## Consequences

- The router walks a list instead of ranking; prices stay in the catalogue
  for the daily budget (R-RTR-1).
- The config is longer: a model used for two purposes is named twice.
- The memory check behaves as under ADR-0047: Haiku first, Sonnet when
  Haiku is out, closed when both are. It now says so in the file instead
  of following from the tiers.
- Every call site names its purpose: turns, scheduled tasks, compaction,
  the clean-context check (ADR-0034), and in P3 module and skill writing.
- `models.example.yaml`, the owner's `models.local.yaml`, chatlab's
  catalogues and the skills' frontmatter are rewritten; `tier` and
  `key_env` fail the start ([ADR-0059](0059-node-config-file.md)).
- If the claude CLI takes no effort, the example above loses its
  `effort` fields for the `claude` models; OpenAI-compatible providers keep
  them.

## Revisit when

- P3: the `code` purpose is used and measured.
- A use fits none of the five purposes (embeddings, vision, a classifier).
- The router learns from outcomes (layer 5) and the order should adapt.
