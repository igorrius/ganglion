# ADR-0044: Serve read-only diagnostics as `ganglion mcp` over stdio, with the own MCP server

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.9](../spec.md#79-observability) (R-OBS-5, R-OBS-6, R-OBS-7); design section 6.5 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md)); [ADR-0019](0019-release-leases-at-core-start.md), [ADR-0037](0037-own-minimal-mcp-server.md), [ADR-0043](0043-logs-on-disk-with-an-own-rotator.md); plan task 6 ([M3c plan](../plans/p1-m3c-modules-skills-diagnostics.md))

## Context

When something is wrong, the owner asks the agent or Claude Code to read
the logs and find the cause. If the agent is down, it cannot diagnose
itself. So the diagnostic path must need no model and no running node.

Everything needed is on the data volume:
- the database: turns, the tool-call journal, the queues;
- the log files (ADR-0043).

Claude Code speaks MCP. An MCP server reached by running a command needs
no port: locally, through `ssh`, `docker exec -i` or `kubectl exec`,
the platform authenticates.

The own MCP server (`mcp`, ADR-0037) already serves tools over an
`io.ReadWriter` for the claude CLI bridge.

## Options

### A. `ganglion mcp` over stdio, with the own `mcp` server

A subcommand that opens the database read-only and the log directory,
and serves a fixed set of read-only tools on stdin and stdout.

### B. The same over the official Go SDK

As A, with `github.com/modelcontextprotocol/go-sdk`.

### C. MCP over HTTP inside the running node, with an admin token

A Streamable HTTP endpoint on the node.

## Comparison

| Criterion | A. Own, stdio | B. SDK, stdio | C. HTTP in the node |
|---|---|---|---|
| Works when the node is down | Yes | Yes | No |
| Open port | None | None | One more, with a token to keep |
| Authentication | The platform's (ssh, docker, kubectl) | Same | Ours |
| Code and size | Reuses `mcp` (ADR-0037) | +7 direct modules, several MB (measured for ADR-0037) | A transport we do not have |
| Fits the agent's tools | The same `provider.Tool` and `Exec` | Typed handlers | Same as A |

## Decision

**A.**
- It works when the node does not.
- It opens nothing.
- It adds no code beyond the tools.

The rules:
- **Read-only by construction.** The database is opened with
  `mode=ro` and `query_only`, the path escaped so it cannot add URI
  parameters of its own. Migrations never run, and the command
  never takes the writer's lock (ADR-0019). WAL lets it read beside a
  running core without blocking it.
- **The tools:**
  - `logs.search`;
  - `journal.search`;
  - `turns.failed`;
  - `turn.show`, one turn whole (R-OBS-5);
  - `config.show`, the configuration this process reads, each secret
    only as set or unset;
  - `queues.state`.

  Each result is capped at 16,000 runes, and a query at 200 rows.
- **Clean output.** stdout carries JSON-RPC lines only; logs go to
  stderr.
- **The agent's tools.** The agent gets `logs.search`, `journal.search`
  and `turns.failed` from the same code, for admins only, as outside
  content.

## Security

- **Trust boundaries.** Whoever can run the command can read all
  conversation history in the data directory. It is the same as file
  access, and the owner setup says so.
- **What an attacker controls.** Text inside the logs and the journal:
  fragments of fetched pages, tool arguments, remote errors.
- **Abuse cases:**
  - *Diagnostics change state.* The connection is read-only and the
    tools are SELECTs and file reads.
  - *A secret leaks through `config.show`.* Secrets are shown as set or
    unset; values pass the redactor everywhere else.
  - *Outside text in a log steers the model reading it.* The agent's copy
    of the tools wraps results as data and taints the session. Claude
    Code reading them is the owner's own session.
  - *A huge query exhausts memory.* Row and rune caps; the log search
    keeps a bounded ring per file and skips (and counts) a line over
    1 MiB instead of stopping at it.
  - *The agent's copy leaks one chat into another.* The searches read
    every conversation, so an admin asking in a group could get another
    chat's text into the group's answer. The agent's tools run only in
    the caller's private chat with the bot, and refuse elsewhere.
  - *A secret sits in a value that is not a secret* (a password in a
    `base_url`). `config.show` values and log lines pass the redactor
    too.

## Consequences

- Claude Code can diagnose the node locally and in a container with one
  MCP config line.
- `railway ssh` is checked in M4 task 4.6. If it does not pass stdio
  cleanly, option C returns as its own ADR.
- The query set is part of the diagnostic contract; a schema change
  updates it.

## Revisit when

`railway ssh` cannot carry stdio, or diagnostics need to write
(a repair tool), which would be a separate, approved path.
