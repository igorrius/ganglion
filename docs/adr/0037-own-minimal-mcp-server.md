# ADR-0037: Write a minimal MCP server of our own instead of using the official Go SDK

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.4](../spec.md#74-providers-and-model-access) (R-PRV-6), R-OBS-7 (`ganglion mcp`, M3c), design sections 5.7 and 6.5 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md)); [ADR-0036](0036-mcp-bridge-over-a-per-run-unix-socket.md) (the bridge), [ADR-0013](0013-protocol-envelope-around-stateless-mcp.md) (MCP inside our envelope later)

## Context

The MCP bridge (ADR-0036) needs an MCP server in the core; `ganglion
mcp` (M3c, R-OBS-7) will need one with another tool set. Both speak
stdio framing (one JSON-RPC message per line) and serve tools only. The
methods needed are five: `initialize`, `notifications/initialized`,
`ping`, `tools/list`, `tools/call`. No resources, prompts, sampling,
elicitation, roots, logging, HTTP transport or OAuth.

The server sits on a security boundary: what it reads comes from a
child process that a prompt injection may steer. Reads must be bounded,
a tool name must map back only to a tool on offer, and the arguments
must reach the policy untouched (ruling R18 of task 8).

New dependencies need a reason (CLAUDE.md, conventions), and the
binary's size matters on a Pi.

## Options

### A. Own minimal server (package `mcp`)

JSON-RPC 2.0 over an `io.ReadWriter`, the five methods, a bounded line
reader shared with the relay, concurrent `tools/call` with a cap, a
fixed tool list.

### B. The official Go SDK (`github.com/modelcontextprotocol/go-sdk`, v1.8.0)

The reference implementation maintained with the specification: every
method and transport, protocol revisions up to 2026-07-28, typed tool
handlers with JSON Schema inference, an optional frame limit on its
stdio transport.

## Comparison

Measured on the dev machine (Go 1.27.3, `-ldflags="-s -w"`):

| Criterion | A. Own | B. Official SDK |
|---|---|---|
| Size | +57 KB on the ganglion binary (30.61 → 30.67 MB) | A minimal stdio server with one tool is 8.1 MB against 2.6 MB for a stdlib JSON loop; 46 non-stdlib packages |
| Dependencies | None | `golang-jwt`, `jsonschema-go`, `segmentio/encoding`, `uritemplate`, `x/oauth2`, `x/time`, `x/tools` |
| Methods we need | Exactly the five | All of MCP; the rest has to be left unused and kept off |
| Fits our types | `provider.Tool` and `provider.Exec` as they are; arguments as raw JSON | Typed handlers per tool; raw arguments need its low-level API |
| Bounded reads | The same 8 MiB line reader as the relay | A frame limit to set and check |
| Maintenance | Ours: a new protocol revision is a manual change (about 260 lines) | Upstream follows the specification |
| Code to review | About 400 lines with the relay | The SDK's surface on every update |

## Decision

**A.** The bridge needs five methods of the protocol, and everything it
reads is attacker-steerable: a small server we read in
full is easier to trust than a large one we configure. It adds no
dependency and 57 KB, against megabytes and seven direct modules.

The server speaks revisions 2024-11-05 to 2025-11-25 (the `initialize`
handshake); it answers a client's version when it knows it, otherwise
its newest. Notifications get no answer; an unknown method is `-32601`;
a call without a tool name is `-32602`; a tool name not on offer goes to
the core as it came, where the policy refuses it and journals it (in a
scheduled task, as a call outside the grant, with the same alert as on
the API path); a tool's error, and the policy's, is a result with
`isError: true`.
The arguments pass to the policy as they came, with one normalisation:
absent or `"arguments": null` becomes `{}`. That is the strictest
value `tool.CheckArgs` accepts: an object that names no parameter, so
nothing reaches the policy or the tool that either could misread; a tool
still refuses a call without its required parameters.

## Consequences

- `ganglion mcp` (M3c) reuses `mcp.Server` with its own tools.
- A new MCP revision the claude CLI requires (2026-07-28 drops
  `initialize` for `server/discover`) is our change to make; the live
  check of the bridge catches it.
- Should we need resources, prompts, HTTP or OAuth, the comparison
  changes.

## Revisit when

- The bridge or `ganglion mcp` needs more than tools.
- The CLI's MCP client stops working with the `initialize` handshake.
- The core speaks MCP over the bus (P2, ADR-0013), where the SDK's
  transports and revisions may pay for themselves.
