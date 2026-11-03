# ADR-0036: Give CLI providers the agent's tools over MCP on a per-run Unix socket with a token

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.4](../spec.md#74-providers-and-model-access) (R-PRV-4, R-PRV-6), design section 5.7 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md#57-tools-for-the-claude-cli-the-mcp-bridge-owner)); [ADR-0021](0021-providers-chosen-by-the-operator-claude-cli-included.md) (the claude CLI as a provider), [ADR-0022](0022-haiku-first-during-development.md) (Haiku through the CLI first), [ADR-0031](0031-agent-security-baseline.md) (the security baseline), [ADR-0037](0037-own-minimal-mcp-server.md) (the server code)

## Context

Haiku through `claude -p` answers first (ADR-0022), but the CLI runs its
own tool loop: it cannot return `tool_calls` for the agent to run, as
an API model does. Until now `provider/claudecli` refused requests with
tools, so the router dropped it for every admin turn once the first
tool was registered.

R-PRV-6 asks for the agent's tools inside the CLI's loop under the same
rules as for an API model:

- the core serves the turn's tools for one run, authenticated by a
  per-run grant;
- the session, principal and turn come from the core's own context,
  never from the child process;
- the CLI's own tools stay off;
- policies, taint, the journal and call keys apply in the core; the
  bridge only forwards.

The claude CLI (2.1.317 on the dev machine) takes MCP servers from a
config file (`--mcp-config`), can ignore every other MCP configuration
(`--strict-mcp-config`), allows tools by pattern (`--allowedTools`) and
turns its built-in tools off (`--tools ""`). Its MCP servers are stdio
processes it starts, or HTTP endpoints. OpenClaw solves the same
problem with a loopback HTTP MCP server in its gateway and a per-run
token.

The attacker view: another local user, or a process of the same user
that is not this run, must not reach the core's tools; the token must
not show in `ps`; a crashed CLI must leave nothing behind; a hostile
peer must not make the core buffer without limit.

## Options

### A. Loopback HTTP with a per-run token (OpenClaw)

The core listens on `127.0.0.1:<random port>` for one run and puts the
URL and a bearer token in the CLI's config. Streamable HTTP MCP.

### B. A Unix socket `0600` in a `0700` run dir, a per-run token, and a stdio relay

The core makes a private run dir, listens on `<run dir>/mcp.sock`, and
writes `mcp.json` (mode `0600`) naming `ganglion mcp-relay --socket
<path>` as a stdio MCP server, with the token in its environment. The
relay connects, sends the token as its first line and copies lines both
ways. The core serves exactly one connection with the right token.

### C. A stdio MCP server process of its own

`ganglion mcp-serve` runs the tools in a separate process that the CLI
starts, opening the database and the workspace itself.

## Comparison

| Criterion | A. Loopback HTTP | B. Unix socket + relay | C. Own server process |
|---|---|---|---|
| Who can connect | Every local process of every user; only the token stops them | Only the owner's uid (dir `0700`, socket `0600`), and then the token | The CLI only |
| Policies, taint, journal, keys in the core | Yes | Yes | No: a second process with its own copy of the turn's state, or a new protocol to the core |
| Turn context from the core | Yes | Yes | Has to be passed to the child, which R-PRV-6 forbids |
| Extra process | None | A small relay per run | A full server per run |
| Protocol code | HTTP transport, sessions, SSE | Lines over a socket | Lines over stdio |
| Works in a container or on a Pi | Yes | Yes | Yes |
| Token exposure | Config file and HTTP headers | Config file (`0600`) and the relay's environment; never argv | — |

## Decision

**B.** A Unix socket cannot be reached by other users at all, so the
token is a second lock rather than the only one, and the transport is
newline-delimited JSON-RPC, the simplest MCP framing (ADR-0037). C is
out because the tools would leave the core.

The details:

1. Per run, a dir from `os.MkdirTemp` under the provider's work dir,
   mode `0700`. If `<run dir>/mcp.sock` would be longer than 103 bytes
   (`sun_path` is 104 bytes on macOS, 108 on Linux, NUL included), the
   run dir goes under `os.TempDir()` instead; if it is too long there
   as well, the call fails as unavailable.
2. The socket is chmod-ed to `0600` after listening; `mcp.json` is
   written with `0600`. The token is 32 bytes of `crypto/rand` in hex,
   written only in `mcp.json`'s `env` for the relay; never in argv or
   logs.
3. The CLI runs with `--tools ""`, `--strict-mcp-config`, `--mcp-config
   <run dir>/mcp.json` and `--allowedTools mcp__ganglion__*`. These flag
   names and the wildcard were checked against `claude --help` and a
   live run of 2.1.317.
4. The listener reads the first line of each connection, at most 128
   bytes within 2 seconds, compares it with `subtle.ConstantTimeCompare`,
   serves the first connection that matches and closes every other
   one, before and after. Each connection is authenticated in its own
   goroutine, at most 16 at once, so silent connections cannot hold the
   relay up.
5. The relay (`ganglion mcp-relay`, hidden) does stdio ↔ socket and the
   token, nothing else, and logs nothing. Both it and the server read
   lines through a reader bounded at 8 MiB; a longer line ends the
   connection.
6. The run dir is removed on every path: success, error, a cancelled
   context, a killed or crashed CLI. The serve goroutine and the calls
   it runs are waited for before the provider call returns. On Linux
   and macOS `claude` runs in a process group of its own, and a
   cancelled or timed-out call kills the whole group, so the relay does
   not outlive it.
7. Tools offered are exactly the turn's (`Specs(admin)`, `Offered`
   gating, a task's grant). A turn without tools (a family member's)
   starts no bridge.
8. Each call goes through the request's `provider.Exec`: the agent's
   tool path, with call numbers continuing the turn's count under a
   lock on the turn, so a later call of the same run sees the taint an
   earlier external result set. A confirm-class call returns "awaiting
   confirmation" to the CLI (not an error) and is pending in the state;
   the run ends normally, and the press resumes the conversation
   (ADR-0032).
9. A turn's CLI tool calls are capped at `MaxSteps`, the API loop's
   limit (8 by default); no separate setting. A call past it is refused
   with a fixed error result ("tool call limit for this turn reached;
   answer with what you have"): it is not numbered, run or journalled.
10. A CLI attempt that ran any tool and then failed (a timeout, a
    non-zero exit, an empty answer) does not fall back to the next
    model, which would not know those calls ran. The turn ends with the
    fixed give-up reply ("Sorry, something went wrong with that
    message."), outcome `error` in the trace. An attempt that ran no
    tool falls back as before.
11. The transcript on stdin indents every line of every message after
    its first, in every role and the summary too, and treats CR, NEL
    (U+0085), U+2028, U+2029, VT and FF as line breaks as well as LF:
    neither an approved page nor a family member's message in a shared
    group can start a line like a turn ("User: Igor: …"). A confirmed
    call's outcome, which comes back as a user note, is cut to 4,000
    runes like a tool result.

## Consequences

- Haiku through the CLI answers admin turns with tools; the catalogue
  may say `tools: true` for `claude-cli` models.
- The binary must be runnable by the CLI as `os.Executable()`
  (`claudecli.WithRelay` overrides it).
- The calls the CLI made are journalled and in the turn's trace, but
  the conversation history keeps only the CLI's final answer: the CLI
  does not report its intermediate tool messages. Taint and pending
  calls are in the state, so nothing that guards a later turn is lost;
  a `web.search` hit found in one CLI turn is not a known URL in the
  next one, so fetching it then asks the owner (fails safe).
- A CLI run that ran tools and then failed gives up instead of falling
  back: the owner asks again and sees no tool run twice, at the cost of
  an answer the fallback model might have given.
- A CLI run with tools has the provider's call timeout (60 s) for its
  whole loop.
- The relay inherits the CLI's environment, so another process of the
  same user could read the token from `/proc`; that user can reach the
  socket anyway, which is outside this boundary.
- A SIGKILL of the core itself skips the deferred removal: a stale
  `run-*` dir with its `mcp.json` stays in the work dir. Its token is
  useless, since nothing listens on that run's socket any more; the
  dirs are left for the work dir's cleanup (a deferred M3a minor).

## Revisit when

- A CLI provider runs on another node (the P2 worker on the owner's PC):
  the bridge then crosses the network and needs the bus.
- The claude CLI drops stdio MCP servers or the `initialize` handshake
  (MCP 2026-07-28 is stateless and has `server/discover`).
- A platform without Unix sockets becomes a target.
