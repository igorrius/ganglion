# ADR-NNNN: Title in the imperative ("Use X for Y")

- **Status:** Proposed | Accepted | Superseded by [ADR-NNNN](NNNN-title.md) | Deprecated
- **Date:** YYYY-MM-DD
- **Deciders:** Igor Artasevych
- **Spec:** links to the affected sections of [the spec](../spec.md)

## Context

What problem forces a decision now. Constraints, requirements (`R-*`
ids), facts and measurements. No solution here yet.

## Options

### A. Name

What it is, in two or three sentences.

### B. Name

### C. Name

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Criterion that matters here | | | |

Criteria come from the context: resource use on a Pi, offline operation,
security, effort, lock-in, fit with the phase plan.

## Decision

The chosen option and the one or two reasons that decided it.

## Security

Spec principle 9: every link is hostile, every input is untrusted.

- **Trust boundaries** this decision creates or crosses.
- **What an attacker controls** here. Examples: the network, a web page,
  a model's output, a module, another node, a person in a group chat.
- **Each abuse case and what stops it.** It must be stopped by code, not
  by a prompt.

"No new boundary" is a valid answer, given in one line with the reason.

## Consequences

- What becomes easier.
- What becomes harder or what we give up.
- What has to be built because of this.

## Revisit when

The signal that would make us reopen this decision.
