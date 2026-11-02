# ADR-0020: Keep history in our own neutral format, stored as the conversation state

- **Status:** Proposed
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-4), [7.2](../spec.md#72-conversations-history-and-the-agent-loop) (R-AGT-2), [7.4](../spec.md#74-providers-and-model-access) (R-PRV-3), [11](../spec.md#11-open-decisions) ("History and tool format")

## Context

M3 makes the bot talk through a model. Two questions come first.

**Format.** The spec's open decision "History and tool format" leans
towards our own neutral format with MCP tool schemas, to be decided once
two providers exist. M3 builds the first provider (OpenAI-compatible,
ADR-0007); the Anthropic Messages client comes later (R-PRV-2). History
written now must survive that second client and a change of model.

**Storage.** R-DLV-4: the reply, the new session state and the "processed"
mark are written in one transaction. The queue already does this:
`Complete` writes replies, a `conv_state` row and the done mark together
(M2). If history lived elsewhere, a crash between the two writes would
either lose the turn from history or record it twice when the event is
processed again.

Facts:

- A family conversation is small: tens of messages a day. With compaction
  into a summary (R-AGT-2) the kept history stays under a few tens of
  kilobytes per conversation.
- `conv_state.data` is JSON, checked by `json_valid`, written by
  `Complete` in its transaction.
- Turns are traced separately in `turns` (R-AGT-5); that trace is for
  observability and may be rewritten when a turn reruns.

## Options

### A. Own neutral format, stored as the conversation state

`provider.Message` grows tool calls and tool results in a shape of our own
(role, text, tool calls with JSON arguments, the id a tool result answers).
Tools are described as MCP tool schemas (name, description, input JSON
Schema). The agent keeps `{summary, messages}` as the conversation's state
and hands it to `Complete`, so history moves exactly with the reply.

### B. Own neutral format in a separate `history` table

Same format, one row per message, appended by the agent before
`Complete`. History can be queried row by row, but its write is outside
the reply's transaction.

### C. OpenAI Chat Completions format as the stored format

Store what the first client sends. No translation now; the Anthropic
client and any format change later must convert stored history.

## Comparison

| Criterion | A. Neutral, in state | B. Neutral, own table | C. OpenAI format |
|---|---|---|---|
| R-DLV-4: history moves with the reply | Yes, same transaction | No: a crash between writes loses or doubles a turn | Depends on storage (as A or B) |
| A second provider or a model change | Translate at the edge | Translate at the edge | Convert stored data |
| Querying single messages (for `memory.search`) | Through `turns`, not history | Direct | Through `turns` |
| Size per write | Whole state (small, bounded by compaction) | One row | Whole state |
| New code | Codec and translation in the client | Table, migration, codec, translation | Least now, most later |

## Decision

**A.** It is the only option where a crash cannot split a turn from its
history without extra machinery, and it keeps the provider format at the
edge, where the second client will need it anyway. Search over past turns
goes through the `turns` trace (R-AGT-5), not through history.

## Consequences

- `provider.Message` carries `ToolCalls` and `ToolCallID`; `ChatRequest`
  carries `[]provider.Tool` (MCP shape). Each client translates both ways.
- Capabilities and prices are per model, not per provider: `Caps()` leaves
  the `Provider` interface and moves to the router's model catalogue.
- The agent's state is versioned JSON (`{"v":1,"summary":…,"messages":…}`);
  a later version reads `v:1`.
- A state that grows past its budget is compacted in the same turn; the
  summary is written by a cheap model.
- The spec's open decision "History and tool format" is resolved by this
  ADR once accepted.

## Revisit when

History must be shared across nodes (P2, JetStream), or per-message
queries over history become a hot path.
