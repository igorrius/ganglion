# ADR-0049: Ask before a read script takes a tainted session's input, keep a local-only session's tools on the node, and mark `ganglion mcp`'s results as data

- **Status:** Proposed
- **Date:** 2026-11-05
- **Deciders:** Igor Artasevych
- **Spec:** [7.3](../spec.md#73-model-router) (R-RTR-4, R-RTR-5), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-6), [7.9](../spec.md#79-observability) (R-OBS-7)
- **Supersedes, once accepted:** the rule of [ADR-0042](0042-skills-and-scripts-before-the-sandbox.md) that a read script always runs by itself, and its "not stopped" on a local-only session's tool calls; the line of [ADR-0044](0044-ganglion-mcp-diagnostics-over-stdio.md) that leaves a client's copy of the results unmarked

## Context

The final review of M3c recorded three rulings as risks the code does not stop.
Going over them before the repository opens, with the agent writing skills next,
they share one fault: a document describes a boundary that the code does not hold.

1. **A read script in a tainted session.** `effects: read` says what the script
   does to the node. It says nothing about the network. A page tells the model to
   put the session's data into the input of a script that calls a web API, and the
   data leaves with no question. Every other way out already asks in a tainted
   session (R-POL-6).
2. **A local-only session's tools.** Routing and compaction stay on local models
   and recall leaves such a session out, but the model can still pass the skill's
   data to `web.search`, `web.fetch` or `memory.write`, whose check goes to the
   guard's model and whose text outlives the session. A promise of privacy that
   does not hold is worse than none.
3. **`ganglion mcp` in a client's session.** Logs and the journal quote pages. The
   agent's copy of these tools wraps results as data, but an outside client gets
   them bare, and the owner's Claude Code session may hold a shell.

## Options

### For the read script

- **A. Ask when the call carries input in a tainted session.** A script with no
  input runs by itself, since nothing of the session reaches it. A scheduled task
  keeps its grant (R-POL-8), approved when the task was made.
- **B. Ask for every read script in a tainted session.** Simpler, but a script
  with no input has nothing to leak, and it would ask after every page read.
- **C. Wait for the M3d sandbox** with no network. Nothing changes until M3d, and
  the sandbox has its own open question (ADR-0039 waits for a spike).

### For a local-only session

- **D. Deny by default.** `tool.Spec.LocalSafe` says a tool sends nothing off the
  node. In a local-only session the policy refuses every other tool, whatever a
  grant says. A new tool is refused until someone decides.
- **E. A list of tools that leave.** `web.fetch`, `web.search`, `memory.write`.
  Smaller, but a tool added later leaks by default, and this list already misses
  `schedule.create`, whose prompt runs later in a session that is not local-only.
- **F. Ask instead of refusing.** A press does not make the promise true: the
  skill's author asked for the data to stay.

### For `ganglion mcp`

- **G. Wrap every result as data, and say so in the server's instructions.** About
  ten lines. It lowers the risk and does not remove it: the wrapper is a
  convention the reading model follows, not a boundary.
- **H. Leave it and warn in the docs.** Nothing in the session says which text is
  outside text.
- **I. Remove quoted text from the results.** Diagnosis needs what the page said.

## Comparison

| Criterion | A | B | C | D | E | F | G | H | I |
|---|---|---|---|---|---|---|---|---|---|
| Closes the path now | Yes | Yes | No | Yes | For today's tools | No | Partly | No | Yes |
| Holds for a tool added later | Yes | Yes | Yes | Yes | No | No | Yes | Yes | Yes |
| Cost in daily use | A press with input | A press every time | None | None today | None | A press | None | None | Weaker diagnosis |

## Decision

**A, D and G.** They close each path in code where code can, at a price nobody pays
yet: no skill in the repository uses `local-only`, the example catalogue has no
local model, and the only built-in skill has no script.

- **`tool.Spec.AskTaintedInput`,** set on a read script, makes the policy confirm a
  call with non-empty or unreadable `input` in a tainted session. The rule runs
  after the grant's, so a task's granted script still runs alone.
- **`tool.Spec.LocalSafe`** is set on the read tools over the node's own data
  (`cells.get`, `memory.read`, `memory.recall`, `schedule.list`, `skill.read`,
  `metrics.show`, the agent's three diagnostics) and on the script of a skill that
  is itself `local-only`, whose owner asked for that. `memory.audit` reads too, but
  its check goes to a model, so it stays off. The policy's rule runs before the
  grant's, and the model reads why it was refused. `TestLocalSafeToolsAreTheListed`
  holds the list.
- **`diag.ExecForClient`** wraps a result as data for `ganglion mcp`, and the server
  sends `diag.ClientInstructions` in `initialize`. The agent's copy stays as it is:
  the agent wraps what the tool marks as outside content.

A reply in a local-only session still goes out through the transport, as it did:
the person asking for the data is who gets it. Sending it anywhere else is refused
like any other tool.

## Security

- **Trust boundaries:** none new. Each option makes an existing one hold.
- **What an attacker controls:** a page or any outside text in a session, so what
  the model puts in a call's arguments; text in logs and the journal.
- **Abuse cases:**
  - *An injection puts the session's data in a script's input.* The policy asks,
    and the question quotes the whole input; an input too long to show is refused.
    `TestTaintedInputWaitsForThePress` feeds this.
  - *The model passes an `input` that is not a string to look empty.* Anything but
    an absent or empty string counts as input.
  - *A read script is granted to a scheduled task.* It runs alone, as R-POL-8 says:
    the owner approved that grant in plain words. The task's turn is tainted by
    its fetches, so a grant should name such a script only knowingly.
  - *A local-only skill's data goes to a tool.* Refused before the grant is read.
    `TestLocalOnlySessionKeepsDataOnTheNode` feeds this.
  - *A call parked before the session became local-only is approved after.* It runs:
    its arguments were written before the model saw the skill's data, and approved
    calls do not pass the policy again.
  - *Another skill's script in a local-only session.* Refused unless that skill is
    local-only too.
  - *Text in a log closes the wrapper.* `tool.WrapExternal` breaks a closing tag
    inside the text.
  - *Text in a log steers the client's model anyway.* **Not stopped:** the wrapper
    is a convention. The client's own confirmations hold the shell, so the docs say
    not to run diagnostics in a session that approves commands by itself.

## Consequences

- A read script that takes input asks once per call after a page was read in the
  session. An input longer than a question can show (3,000 characters) is refused in
  a tainted session, as it already is for a change script.
- A new tool is off in a local-only session until it declares `LocalSafe`: a little
  more to decide per tool, and a wrong default fails closed.
- The model in a local-only session cannot store a memory or schedule anything. The
  owner can start a new session with `/new`.
- Clients of `ganglion mcp` read JSON inside a wrapper. Nothing parses it today.
- On acceptance, ADR-0042 and ADR-0044 name this ADR in their status.

## Revisit when

- The M3d sandbox holds a script off the network: A may relax for such a script.
- The agent creates skills: `local-only` and `AskTaintedInput` are part of what a
  check of a drafted skill must read.
- A local tool has a real need in a local-only session: it declares `LocalSafe`.
