# ADR-0007: Start with an OpenAI-compatible provider client

- **Status:** Accepted
- **Date:** 2026-10-31
- **Deciders:** Igor Artasevych
- **Spec:** [7.4](../spec.md#74-providers-and-model-access)

## Context

0.x limits us to three providers: one cloud API, Ollama, and the claude CLI
worker. Routine turns (conversation, crons, consolidation) should run on a
cheap model over an API. We need one client first and want it to cover as
many backends as possible.

Note: "OpenAI-compatible" means OpenAI's Chat Completions wire format, a de
facto standard; it is unrelated to OpenAPI (Swagger), which describes REST
APIs in general.

## Options

### A. Anthropic Messages API

### B. Google Gemini API

### C. OpenAI-compatible Chat Completions

## Comparison

| Criterion | A. Anthropic | B. Gemini | C. OpenAI-compatible |
|---|---|---|---|
| Backends covered by one client | Anthropic | Google | OpenAI, Ollama, vLLM, OpenRouter, DeepSeek, Gemini's compatible endpoint |
| Covers the local-model requirement | No | No | Yes, via Ollama |
| Tool calling | Yes | Yes | Yes |
| Cheap model choice | Limited to one vendor | Limited to one vendor | Wide |

## Decision

**C.** One Go client covers both the cloud provider and Ollama, which
satisfies `privacy: local-only` and leaves a wide choice of cheap models.
The native Anthropic Messages client comes second, when needed. Which cloud
model handles routine turns is decided on cost and quality of real turns in
P1 (spec section 11).

## Consequences

- History is stored in a neutral format and translated per provider
  (R-AGT-2); MCP tool schemas are translated to the API format (R-PRV-3).
- Provider-specific features (prompt caching, extended thinking) wait for
  native clients.

## Revisit when

A provider we need drops or breaks compatibility, or routine turns need a
feature only a native API offers.
