# ADR-0099: The CLI bridge announces tools turned on during a run

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** Igor Artasevych
- **Spec:** [R-MCP-4a](../spec.md), [R-PRV-6](../spec.md)

## Context

With the `claude` CLI provider the CLI runs its own tool loop for a
whole turn over the MCP bridge (ADR-0036, ADR-0037). The bridge's tool
list was fixed when the run started and it declared
`listChanged: false`. R-MCP-4a makes a server's tools reach the model on
demand: `mcp.use` turns them on, and they are callable "from the next
step". The API path rebuilds the list every step; on the CLI path a
whole run is one step of the agent, so the model called a tool it had
just turned on and the CLI answered "No such tool available" (#284).
The tools worked only from the next message.

A check on the real CLI (2.1.327, `--model haiku`, the provider's flags:
`--output-format json --no-session-persistence --tools ""
--strict-mcp-config --mcp-config … --allowedTools mcp__ganglion__*`)
with a stub server that declares `listChanged: true` and sends
`notifications/tools/list_changed` before the answer of the call that
turned a tool on: the CLI sent `tools/list` again right after that
answer, its model called the new tool in the same `-p` run, and
`--allowedTools mcp__ganglion__*` covered it (no permission denial).

## Options

### A. Announce the change on the bridge

The bridge's list follows the turn: after each call it takes the turn's
list again (the same rules as the API path's step list) and, when it
changed, serves the new list and sends `list_changed` ahead of the
call's answer. The model's next step inside the run has the tool.

### B. End the run after a call that turned tools on

The agent ends the CLI run (as it does for an addition, R-LIVE-3) and
starts the next step with the new list; the calls that ran go into the
history, and the next run reads them as a transcript.

## Comparison

| Criterion | A | B |
|---|---|---|
| Works with the real CLI | yes, checked | yes |
| Cost | none | a new CLI run: the whole transcript sent again, a process start |
| What the model lost | nothing | its unsaid text in the ended run |
| Code | a list function on the request, an option of the MCP server | a new end cause and its accounting in the agent |
| Depends on the CLI | on `list_changed`, part of MCP | no |

## Decision

A. The CLI supports `list_changed` and the change costs nothing per
turn; B throws a run away for every `mcp.use`. If a CLI release stops
re-listing, the tool answers "No such tool" again and B is the fallback.

The provider request carries `ToolsNow`, the turn's list as it stands;
the agent gives the base list of the turn plus the on-demand tools the
session has on now, under the same offer rules (admin, a task's grant,
the persona tool outside the introduction). Handing the list out moves
the turn's `On` with it: on the CLI path the model's next step is the
next call after the list changed.

## Security

No new boundary. The bridge offers only what the agent's list gives; a
name not offered still goes to the core, whose registry refuses an
on-demand tool not in `Call.On`, and `Call.On` grows exactly when the
bridge's list does. The list is the agent's, built from the approved
list; nothing the server or the model sends changes it. The
notification carries no data.

## Consequences

- "Turn on X, then call X" works within one turn on both paths.
- The bridge's list is guarded by a lock and compared after every call.
- A CLI that ignores `list_changed` keeps the old behaviour.

## Revisit when

A CLI release stops listing again on `list_changed`, or the CLI path
gets a persistent session in which a turn spans several runs.
