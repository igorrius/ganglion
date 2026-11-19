# ADR-0100: On-demand bookkeeping spends no turn budget, and the budget is 20

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** Igor Artasevych (the owner)
- **Spec:** [R-AGT-3](../spec.md), [R-MCP-4a](../spec.md)

## Context

[ADR-0036](0036-mcp-bridge-over-a-per-run-unix-socket.md) fixed a turn's
budget at 8: on the API path 8 model calls, on the `claude` CLI path 8
tool calls (`MaxSteps`). Plan calls were already taken out of it
([ADR-0052](0052-a-plan-is-delivered-during-its-turn.md)) and given a
ceiling of their own.

R-MCP-4a makes a server's tools reach the model on demand: before it can
read anything, the model calls `mcp.tools` to list a server's tools and
`mcp.use` to turn some on. In the live check of #261 a question about a
month's purchases ended with "tool call limit for this turn reached": 4
of the turn's 8 calls were `mcp.tools` and `mcp.use`, and the read the
answer needed never ran (#286).

Two facts shape the fix. First, `mcp.tools` and `mcp.use` send nothing
off the node (`LocalSafe`) and move nothing: they are bookkeeping, not
work. Second, a step is not a call on the API path: a step is one model
call, with any number of tool calls in it, while on the CLI path the
budget counts tool calls.

## Options

### A. Raise the number only

8 becomes 20 and a node setting. Bookkeeping still spends the budget; a
server with many tools or a model that lists twice still eats a part of
it, only later.

### B. Exempt the on-demand calls only

`mcp.tools` and `mcp.use` spend no budget, on either path, with a ceiling
of their own so a model cannot loop on them. The number stays 8.

### C. Both

The exemption of B, and 20 as a default that a node may change in its
file.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| The reported turn finishes | yes, by luck of the margin | yes | yes |
| Bookkeeping can still starve the work | yes | no | no |
| A model that only lists is stopped | by the budget | by its ceiling | by its ceiling |
| Room for a real multi-read question (8 is tight with plan and search calls too) | yes | no | yes |
| A node with a cheap model and a tight budget can lower it | yes | no | yes |
| Code | a field and a setting | a ceiling and an exemption | both |

## Decision

C. The exemption removes the cause; the larger, configurable number
answers the same turn with 8 calls of real work, which a multi-step read
of a connected server needs, and lets a node choose its own cost.

- `MaxSteps` defaults to 20 and is the node setting `agent.max_steps`,
  from 1 to 100, checked at the start. It is not changed from the chat:
  it widens what a turn may spend ([ADR-0093](0093-config-changes-through-the-agent-with-history-in-the-workspace.md)'s
  list is closed on purpose).
- `mcp.tools` and `mcp.use` have a ceiling of their own, 16 calls a turn
  (`maxOnDemandCalls`, the size of a session's on-demand set). A call
  past it is refused with "no more tool listing or turning on in this
  turn; go on with the work using the tools you have", which, unlike the
  budget's refusal, does not tell the model to stop.
- On the CLI path such a call is not counted in the tool-call budget.
- On the API path the budget counts model calls, so a call cannot be
  exempted alone: a step whose every call is an on-demand one that ran
  is not counted in the budget (the turn's recorded steps still count
  it, for the trace and cost). A step with any other call counts as
  before. A refused call did not run, so a model that keeps listing past
  the ceiling spends steps again and ends at the limit: the loop is
  bounded by the ceiling plus the budget.

This amends the number in ADR-0036 (8 calls); ADR-0036 itself stands.

## Security

No new boundary. Both tools are `LocalSafe` and offer only the admin's
approved list; the exemption lets a turn make at most 16 more local calls
and no more model calls than before the ceiling. What the model controls
is how often it lists: bounded by the ceiling. A larger default raises
the cost a hostile prompt can cause in one turn by 12 calls, still inside
the turn's timeout and the daily limits (R-AGT-4); a node lowers it with
`agent.max_steps`.

## Consequences

- A turn that lists and turns on tools keeps its full budget for the work.
- A node's file sets the budget; the guide says so.
- The API loop has a second counter beside the trace's steps.
- A turn can end up to 16 on-demand calls past what the trace's step
  count suggests; `Steps` in the turn's trace stays the real number of
  model calls.

## Revisit when

A real turn hits the on-demand ceiling or the 20 calls while doing
sensible work, or the CLI path counts model steps instead of calls.
