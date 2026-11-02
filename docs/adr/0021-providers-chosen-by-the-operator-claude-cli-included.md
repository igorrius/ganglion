# ADR-0021: Let the operator choose one or more providers, the claude CLI included, for conversation

- **Status:** Proposed
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.3](../spec.md#73-model-router), [7.4](../spec.md#74-providers-and-model-access) (R-PRV-1, R-PRV-4)
- **Supersedes in part:** [ADR-0009](0009-claude-subscription-only-via-cli-worker.md) (its rule that conversation turns never go to the subscription)

## Context

The M3a plan gave the agent one kind of provider: an OpenAI-compatible
API (ADR-0007). The owner's case is different. Their node should talk
through the claude CLI they have installed and logged in locally, and
another operator may use one API, several, a local model, or a mix.

Two earlier decisions stand in the way:

- **R-PRV-4 and ADR-0009:** the CLI suits standalone tasks, not
  conversation, because `--resume` does not work with history kept by the
  core.
- **ADR-0015:** it moved the CLI worker to P2.

Facts, checked 2026-11-02 with Claude Code 2.1.316:

- `claude -p` needs no session to hold a conversation. The core keeps the
  history (ADR-0020) and can pass it in every call. The probe call used
  `--no-session-persistence --tools "" --strict-mcp-config --setting-sources ""`
  with a system prompt.
- Haiku answered a short Russian question in 2.9 s end to end (1.2 s of API
  time). `--output-format json` returns the text, token usage and a
  notional `total_cost_usd`.
- Prompt caching works without a session, because the cache matches on the
  prompt prefix, not on the session. Two calls were made with the same
  ~4.7k-token system prompt. The second call read 4,732 tokens from the
  cache and wrote 645: the transcript, which is one text block on stdin and
  is written to the cache again on every call. The CLI writes 1-hour cache
  entries.
- `--resume` would cache the history too. It would also make the CLI's
  session a second copy of the history next to the core's (ADR-0020), and
  that copy goes wrong in three ways:
  - a turn rerun after a crash already sits in the session;
  - turns answered by a fallback provider are missing from it;
  - the core's compaction never reaches it.

  The history block is capped by compaction (about 6k tokens), so the
  saving is small.
- Each call starts a Node.js process of a few hundred MB for a few
  seconds. That memory belongs to the host, not to the node's 128 MB
  (principle 8).
- ADR-0009 records the terms: logging in to an unmodified `claude` binary
  with one's own subscription is allowed. Pro/Max limits assume ordinary
  individual use, and a 24/7 family bot on one subscription "does not
  clearly fit that". That remains true.

## Options

### A. Only OpenAI-compatible APIs in M3 (the plan as written)

The owner talks through OpenRouter. The CLI waits for P2 and heavy tasks.

### B. Provider types in the catalogue; `claude-cli` is one of them

The model catalogue lists providers with a `type`: `openai`
(`base_url`, `key_env`) or `claude-cli` (`command`, default `claude`). The
operator lists one provider or several, and the router ranks their models
as before. The `claude-cli` provider runs an unmodified `claude -p` per
model call:
- the core's history goes into the prompt as a transcript;
- the system prompt and the summary go into `--system-prompt`;
- no session, no settings, no MCP servers, no built-in tools.

In M3a it answers with text only. Tool calls come in M3b, through a
`--mcp-config` that points at the core's own tools.

### C. Anthropic Messages API with a key

This is clean on terms, but the owner has no Anthropic API key. Haiku is
also reachable through OpenRouter under option A.

## Comparison

| Criterion | A. API only | B. Provider types, CLI included | C. Anthropic API |
|---|---|---|---|
| The owner's setup (local claude) | No | Yes | Needs a key they do not have |
| Several providers at once, fallback between them | Several OpenAI-compatible APIs | Any mix | Any mix, once built |
| Terms | Clean | Allowed use of an unmodified binary; family-scale traffic on one subscription is the owner's risk (ADR-0009) | Clean |
| Cost per turn | Per token | Inside the subscription's limits | Per token |
| Latency | ~1 s | ~3 s: a process start per call | ~1 s |
| Tools in M3b | Native tool calls | Through MCP config: more work | Native |
| Where it runs | Anywhere | Only where `claude` is installed and logged in (the owner's PC, not a small edge or a bare container) | Anywhere |

## Decision

**B.** The operator decides which providers a node uses, and the owner's
real setup is the local CLI. Option A stays inside B: an `openai` provider
in the catalogue. Fallback can mix them: for example `claude-cli` first,
OpenRouter when the subscription is rate-limited or the CLI is missing.

## Consequences

- The spec's R-PRV-4 changes: the CLI may serve conversation, without
  `--resume`, with the core's history in the prompt. Routine work goes to
  whatever the operator configured; a cheap API model is a recommendation,
  not a rule.
- The conversation part of ADR-0009 is superseded. Its other rules stand:
  - the project never asks for, stores or forwards tokens;
  - extracted OAuth tokens and built-in sign-in stay forbidden;
  - one subscription never serves other people's nodes.
- The owner carries the subscription-limits risk ADR-0009 describes.
  Usage-limit answers from the CLI count as rate limits, so the router
  falls back to the next provider when one is configured.
- The `claude-cli` provider limits itself to 2 concurrent processes. Its
  models are priced 0 in the catalogue, so they do not spend the daily
  budget; their tokens still count towards the daily token limit.
- M3a gains a task for `provider/claudecli` and provider types in the
  catalogue. The M3b tool work must cover MCP for the CLI.
- The claude CLI worker for heavy standalone tasks stays in P2
  (ADR-0015); this ADR only adds it as a conversation provider.

## Revisit when

- Anthropic's terms or the CLI's flags change.
- The subscription's limits stop the family bot often.
- The owner gets an API key, which would make option C cheaper to run
  than process starts.
