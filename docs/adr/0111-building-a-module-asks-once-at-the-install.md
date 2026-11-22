# ADR-0111: Build modules without a press on the way, and ask once, at the install, for every module and link of the request

- **Status:** Accepted
- **Date:** 2026-11-22
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-17), [R-AGT-4](../spec.md), [R-POL-6](../spec.md), [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules) (R-BUS-5); supersedes the tainted-session press of [ADR-0075](0075-reading-a-modules-state-keeps-the-session-clean.md) (option C) and the confirmation of `module.import` from [ADR-0061](0061-local-modules-built-from-source.md)

## Context

The M9 live check asked the agent for two small linked modules. The owner pressed about thirteen
cards and got one module of two; the second was never built. The node's journal shows why:

- `module.import` asked once per template (two presses).
- A build's answer (the trial's messages are the module's text) tainted the session. From then on
  every `module.write` asked first (ADR-0075, option C): eight presses for a manifest, examples and
  code being fixed in a loop, each showing a file the owner cannot judge on a phone.
- Each press resumes the agent one level deeper (`it.Depth + 1`, R-AGT-4). After three presses in a
  row the next `module.build` was beyond `MaxDepth` 3 and forbidden, so the second module stopped
  half done.
- `module.install` asked once per build.

The owner's word (2026-11-22): a request to build a module ends with the modules built and
installed; the node asks no questions on the way; at most one press at the end confirms the install
of everything built. ADR-0067 already said a press per write "turns a fix loop into a dozen
confirmations without telling the owner more than the window does"; ADR-0075 brought that press back
for tainted sessions, and the tainted state is the common one once a trial has run.

Facts that bound the risk: a module's source does nothing until it is installed; a build writes only
its temporary folder (ADR-0064); the trial runs the real `.wasm` in a sandbox with canned HTTP
(R-RUN-19); a module reaches only what its manifest grants (R-RUN-1); the install window shows those
rights and the trial's results.

## Options

### A. Keep the presses on the way

Today's flow, with the depth fix alone. Every write in a tainted session, every import and every
install still asks.

### B. One press at the start

The agent asks once "build modules X and Y?" and is free until the install window, which still asks
per module.

### C. No press on the way, one install window for the whole request

Import, write and build never ask. The agent fixes and rebuilds until the trials pass, then calls
`module.install` once with every build and every link of the request. One window shows each module's
rights and trial and the links; one press installs all.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Presses for two linked modules | ~13 (today) | 3–4 | 1 |
| What a press shows that the window does not | a file the owner cannot judge | a name | — |
| Code runs before the owner's press | no | no | no |
| Outside text can shape code without a press on the way | no | after one press | yes, shown in the window |
| A fix loop ends without the owner | no | yes | yes |

## Decision

**C** (owner, 2026-11-22): the install window is the trust boundary; presses on the way cost the
owner's attention and tell nothing the window does not.

- `module.import` copies a shipped template without a press (admin-only, an empty folder; its files are
  the node's own, as today).
- `module.write` writes without a press in any session. Provenance stays (ADR-0067, ADR-0068): a file
  written from a tainted session is not the node's own, so builds and reads of that folder answer as
  outside content and keep the session tainted. A tainted call that reaches the tool with no person
  behind it (a scheduled task's) is still refused.
- A resume after the owner's press keeps the depth of the call that asked (a press is the owner's
  answer, not the agent calling itself). `MaxBuildsInARow` (ADR-0070) still ends a fix loop that does
  not converge.
- `module.install` takes one to five builds and the links between their ports and the installed
  modules. One window lists, per module, its rights, what widened, its trial, and a line when its
  source is not the node's own ("written after the agent read outside content"), then the links with
  their shapes. One press installs the builds in order and writes the links; each is checked again
  at the press; a refusal names what failed, and what was already installed stays.
- The module-writing skill builds, fixes and rebuilds without narrating each step, and ends with one
  `module.install` for everything the request built.

## Security

- **Trust boundary:** text the agent read → module code → the install press → a running module.
- **What an attacker controls:** outside text in the session (a fetched page, a file, a module's own
  messages in a trial).
- **Abuse cases:**
  - Injected text dictates a module that leaks data → nothing runs before the press; the window shows
    the hosts and secrets it gets, and says its source was written after outside content; the
    trial runs sandboxed with canned HTTP; least-rights rules stay in the skill.
  - Injected text asks for a link that sends a module's data somewhere → links join only modules on
    this node and are shown in the same window with their shapes.
  - A loop that builds forever → `MaxBuildsInARow` per module, one build at a time per node.
  - A press that deepens the chain without bound → a press needs a person every time; depth still
    bounds agent-started turns.

## Consequences

- R-RUN-17 loses the tainted-session press; R-AGT-4 says a press keeps the depth; the install window
  (R-RUN-10, R-RUN-19, R-BUS-5) covers several builds and links.
- The window must stay readable for up to five modules: per module a short block, the trial as a
  count with failures named.
- `link.add` stays for a link between modules that are already installed.

## Revisit when

A module can be installed from outside this node (edges, M10), or the window grows past what a phone
shows.
