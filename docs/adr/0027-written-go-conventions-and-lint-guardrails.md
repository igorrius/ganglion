# ADR-0027: Write the Go conventions down and enforce what a linter can

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [2, principles 5 and 6](../spec.md#2-principles); [plan P1](../plans/p1-all-in-one.md)

## Context

After M3a the code is about 9,500 lines of Go outside tests, most of it
written by models in short sessions. A review on 2026-11-02 found the
package structure sound, but the code inside the packages does not hold
together:

- `agent.Handle` is one function of about 120 lines. It decodes the event,
  handles sessions and commands, checks limits, builds the prompt, runs
  the model and tool loop, compacts and stores. Two closures capture its
  state, and a mutable trace record travels through `call`, `compact` and
  `sessions`. `sessions` takes seven arguments and returns four values.
- Closed sets of values are bare strings: message roles, turn outcomes,
  event kinds, tiers, transport names.
- The same knowledge is written several times:
  - the admin alert fan-out three times;
  - call cost, start of day, backoff with jitter, rune truncation and the
    plain transcript twice each.
- Some knowledge sits in the wrong package: the Telegram private chat of a
  principal lives in `node`; the provider factory is a `switch` inside
  `node`.

M3b puts memory, tools, policies, skills and the depth limit into exactly
this code. Each model session sees only part of the code. Without written
rules, each session copies the nearest example, and the nearest example is
`Handle`.

## Options

### A. Written conventions plus lint guardrails

A conventions page (`docs/conventions.md`) linked from `CLAUDE.md`, so
every session reads it. It covers:
- stage pipelines and middleware;
- typed values;
- named function types for dependencies;
- `internal/` for shared helpers;
- one source for each piece of knowledge.

`golangci-lint` enforces what it can: `funlen`, `gocognit`, `dupl`,
`goconst`, `exhaustive`, and `depguard` for the composition-root rules.

### B. Lint guardrails only

The same linters with no page. Limits on length and complexity stop the
worst growth, but they do not say what to do instead. A model splits a long
function into arbitrary pieces to satisfy `funlen`.

### C. Review per change

No page and no new linters. Each milestone review catches what it catches.
That is today's state, and it produced the code above.

## Comparison

| Criterion | A. Page + linters | B. Linters only | C. Review |
|---|---|---|---|
| A model session knows the expected shape before writing | Yes | No | No |
| Growth of long functions stopped mechanically | Yes | Yes | No |
| Says what to do instead (pipeline, middleware, table) | Yes | No | After the fact |
| Cost | A page to keep in sync; lint fixes | Lint fixes | Review time, repeated |

## Decision

**A.** The page is short enough to read every session. The linters fail CI
on the patterns that grew `Handle`.

The new linters are switched on by the [refactoring plan](../plans/p1-refactor.md)
once the code passes them, not before: CI stays green on every commit.

## Consequences

- `docs/conventions.md` exists; `CLAUDE.md` and `docs/README.md` point to
  it.
- A refactoring before M3b brings the code in line
  ([plan](../plans/p1-refactor.md)). Behaviour, metrics, log lines and the
  database schema do not change.
- A rule that turns out wrong is changed on the page in the same commit as
  the code.
- Limits like `funlen` sometimes need a `//nolint` with a reason, for
  example in table-driven tests or a long `switch` over protocol codes.

## Revisit when

- The `//nolint` comments for one linter keep piling up: its threshold is
  wrong.
- Reviews keep finding a pattern the page does not name.
