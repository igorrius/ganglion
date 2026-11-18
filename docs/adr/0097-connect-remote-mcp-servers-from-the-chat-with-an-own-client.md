# ADR-0097: Connect remote MCP servers from the chat with an own client and `x/oauth2`, in P1

- **Status:** Proposed
- **Date:** 2026-11-18
- **Deciders:** Igor Artasevych
- **Spec:** [7.16](../spec.md#716-remote-mcp-servers) (R-MCP-1…10), [7.14](../spec.md#714-node-configuration) (R-CFG-6, R-CFG-14), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-4, R-POL-6), [9.8](../spec.md#98-self-modification-levels), [14](../spec.md#14-not-now); [design note](../design/mcp-client-and-secret-intake-design.md); [ADR-0017](0017-extension-points.md), [ADR-0037](0037-own-minimal-mcp-server.md), [ADR-0093](0093-config-changes-through-the-agent-with-history-in-the-workspace.md), [ADR-0098](0098-hand-values-over-from-the-chat-through-a-pinned-static-page.md)

## Context

The owner uses the agent daily and asked it to connect a public MCP
server: a retailer's, which signs a person in with OAuth 2.1, PKCE and
dynamic client registration over Streamable HTTP. The node cannot: it
serves MCP (the bridge, `ganglion mcp`) but is no MCP client. The spec
plans MCP modules as level 3 in P3 (9.8), and two "Not now" items already
wait on a client (the OpenClaw importer's servers, the platform's own MCP
server).

Constraints and facts:

- **Provider-neutral.** The claude CLI's own MCP connectors belong to one
  provider, and the node runs the CLI with `--strict-mcp-config` and
  `--tools ""` (ADR-0036), so it never sees them. A server connected to
  the node must work with any provider (owner, 2026-11-18).
- **No shell.** A node from a platform template has no shell for its admin
  and may have no public address; Telegram is read by polling.
- **A module is the wrong shape.** A module can give the agent tools and
  reach a host, but its tools are fixed in its manifest while a server
  lists its own, it cannot run an OAuth flow, and handling a server's
  credentials and its outside text is trust the core keeps (ADR-0017).
- **ADR-0037** chose an own minimal MCP server over the official Go SDK
  for its size and its seven direct dependencies, and named the case to
  revisit: HTTP or OAuth needed. A client needs both.
- **What a remote client needs:** `initialize`, `tools/list`,
  `tools/call` and the `list_changed` notification; the session header; an
  answer as JSON or as an event stream; OAuth discovery (RFC 9728, RFC
  8414), dynamic registration (RFC 7591), PKCE, the code exchange and
  refresh, the `resource` parameter (RFC 8707).
- The current phase is P1; the work goes in now (owner, 2026-11-18).

## Options

### A. Own client, standard library only

The MCP client and all of OAuth written in the repository: about 1,000
lines, no dependency.

### B. The official Go SDK

`github.com/modelcontextprotocol/go-sdk` as the client, with its OAuth
support.

### C. Own client, `golang.org/x/oauth2` for the token flow

The MCP client our own (the protocol subset is small and the server side
is already ours); the code exchange, PKCE and refresh from `x/oauth2`;
discovery and registration our own (about 150 lines).

## Comparison

| Criterion | A. Own, stdlib | B. Official SDK | C. Own + `x/oauth2` |
|---|---|---|---|
| New dependencies | None | 7 direct modules (ADR-0037's measure) | One, from the Go team, no transitive modules beyond the standard library |
| Binary size | Smallest | Megabytes (ADR-0037: 8.1 MB against 2.6 MB for a one-tool server) | Tens of KB |
| Risk in the token flow | All ours: easy to get a detail of OAuth wrong | Upstream's | The exchange, PKCE and refresh are a library used for years |
| Code to review on the attacker-steered path | About 1,000 lines | The SDK's surface on every update | About 700 lines of ours plus a small library |
| Fit with `provider.Tool`, the registry and raw arguments | Exact | Typed handlers; raw arguments need its low-level API | Exact |
| A new protocol revision | Our change | Upstream follows | Our change |

How servers are added was settled first (owner): entries in `ganglion.yaml`
written by the agent's own tool after the admin's press, not a module per
server and not the provider's connectors.

## Decision

**C** (owner, 2026-11-18), taken into P1 as its own milestone. It keeps
the part the attacker steers small and ours, as ADR-0037 asked, and takes
the part where a mistake is easiest, the token flow, from a library that
has had years of use.

- **The file.** `mcp.servers.<name>`: `url`, `auth` (`none`, `oauth`,
  `header` with `header:` naming it), `timeout`, `tools.<tool>` with a
  policy (`auto`, `confirm`, `forbidden`), and the `fingerprint` of the
  approved tool list. `mcp` applies at once, like `modules`, and only its
  own tools change it, never `config.propose`.
- **Connecting.** The agent's `mcp.connect(name, url[, header])` checks
  the address as `web.fetch` does, probes the server, and for OAuth
  discovers, registers with the intake page as the redirect address, and
  sends a sign-in card through `intake` (ADR-0098). Then it lists the
  tools and sends a card with a proposed rule for each (read-only by the
  server's hint: `auto`; the rest: `confirm`). **Connect** is the
  confirmation of the file's change (ADR-0093); the tools join the
  registry with no restart.
- **Tools.** Named `<server>.<tool>` (a leading `<server>_` dropped),
  admin-only, their results outside content, `AskTaintedInput` set, never
  `LocalSafe`. The policy is the file's. At most 128 tools a server, a
  description of 8,000 characters, a schema of 32 KB, a list of 512 KB; a
  tool past a limit or with a colliding name is skipped and named on the
  card.
- **On demand** (owner, 2026-11-18, after the spike: the first real
  server listed 42 tools in 115 KB, about 30,000 tokens a turn). The agent
  always has `mcp.tools([server])`, an index of the servers and of a
  server's tools (name, policy, first line), and `mcp.use(server, tools)`,
  which turns tools on for the session; their full definitions join from
  the next step. At most 16 tools and 48 KB on at once. Sending every
  approved tool on every turn, and fixing a subset on the card, were the
  other two options: the first fills the context, the second leaves the
  agent without the rest.
- **Credentials.** The registration and the tokens live in the secret
  store under the scope `mcp:<name>`; a key for `auth: header` too.
  Removing a server removes the scope.
- **A changed tool list** turns the server's tools off until the admin
  approves the new list.
- **No retry of a side effect.** A call that is not read-only and gets no
  answer is not repeated; the journal records "outcome unknown".
- **`/mcp`** lists the servers and their state and disconnects one.
- **Local servers as processes** stay level 3 in P3.

## Security

- **Trust boundaries:** a new outbound one, to each server and its sign-in
  server, carrying the admin's access to that service. The tokens are
  inside the existing store; no inbound boundary is added (ADR-0098 covers
  the page).
- **What an attacker controls:** the server's answers and tool
  descriptions; the metadata it points to; the model, steered by outside
  text; a URL someone gives the agent.
- **Abuse cases and what stops them:**
  - *A result carries instructions.* It is outside content: wrapped as
    data, the session tainted, every way out asks (R-POL-6).
  - *A description carries instructions.* The admin sees the list on
    connecting; descriptions are capped; a changed fingerprint switches
    the tools off.
  - *A side effect without the admin.* Everything not marked read-only
    asks by default; only the admin's press changes a rule.
  - *The model sends a token somewhere.* No tool returns a credential; the
    client adds the header only to requests to the server's origin and
    drops it on a cross-origin redirect; values go to the redactor.
  - *A foreign sign-in server.* The card names the sign-in host; every
    metadata address passes the public `https` check; the token is bound
    to the server with `resource`.
  - *A URL that leaks the session.* In a tainted session `mcp.connect` is
    confirmed before it makes a request.
  - *An internal address (SSRF).* The same address rules as `web.fetch`,
    for the server and for every metadata address.
  - *The server asks the node for more.* No capability is declared;
    sampling, elicitation and roots are refused.
  - *Session data leaves through a tool's arguments in a `local-only`
    session.* MCP tools are not `LocalSafe` and are refused there.

## Consequences

- The admin connects a server in the chat, and it keeps working when the
  node changes provider.
- One new dependency, `golang.org/x/oauth2`.
- The two "Not now" items that waited on a client (the OpenClaw importer's
  servers, the platform's MCP server) wait only for their own work; both
  still need a server that runs over HTTP.
- To build: `mcpclient` (client, OAuth, registry source), the `mcp:`
  section and its schema, `mcp.connect`, `mcp.rule`, `mcp.tools` and
  `mcp.use`, the cards,
  `/mcp` and the `/status` line, a chatlab scenario and a live check.
- ADR-0037 is not rewritten: its server stays as it is; this ADR is the
  revisit it foresaw, for the client side.

## Revisit when

- Models handle tool lists of this size without cost (a provider-side
  tool search), so on-demand loading stops paying for itself.

- A server the owner needs runs only as a local process: level 3 comes
  forward.
- People on one node need their own accounts on a server.
- The MCP specification changes its authorization flow, or the
  `initialize` handshake for clients.
