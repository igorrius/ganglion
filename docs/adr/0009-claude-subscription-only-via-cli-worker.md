# ADR-0009: Use a Claude subscription only through an unmodified claude CLI worker

- **Status:** Accepted
- **Date:** 2026-10-31
- **Deciders:** Igor Artasevych
- **Spec:** [7.4](../spec.md#74-providers-and-model-access)

## Context

The author has a Claude subscription and would like heavy tasks (writing a
module, analysing logs) to use it. Anthropic's
[Claude Code legal and compliance terms](https://code.claude.com/docs/en/legal-and-compliance)
(checked 2026-10-31) say products must use API keys, while a user may log
in to an unmodified Claude Code binary with their own subscription. Pro/Max
limits assume ordinary individual use; a 24/7 family bot on one
subscription does not clearly fit that.

## Options

### A. Anthropic API with the user's own key

### B. A worker that runs an unmodified `claude -p`; the user logs in through Anthropic's own flow

### C. Extract the subscription OAuth token and call the API with it

### D. A built-in "Sign in with Claude", or one subscription worker serving other people's nodes

## Comparison

| Criterion | A. API key | B. CLI worker | C. Reuse OAuth token | D. Sign-in / shared worker |
|---|---|---|---|---|
| Allowed by the terms | Yes | Yes, as the user's own use | No | No |
| Project touches tokens | API key only | Never | Yes | Yes |
| Cost | Per token | Within the subscription | — | — |
| Fit for conversation | Good | Poor: `--resume` does not work with core-owned history | — | — |

## Decision

**A** is the default; **B** is an option for the subscription owner, used for
standalone heavy tasks. **C** and **D** are forbidden. Conversation turns
and crons go to a cheap API model rather than the subscription.

## Consequences

- The claude CLI is one worker among others; the system must work without
  it (risk table, spec section 12).
- The worker needs x64/arm64 and a few hundred MB; it lives on the owner's
  PC or server, never on a small edge.

## Revisit when

Anthropic's terms change, or the CLI changes in a way that breaks
`claude -p`.
