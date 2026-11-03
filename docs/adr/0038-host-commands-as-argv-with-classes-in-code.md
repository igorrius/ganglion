# ADR-0038: Give the agent host commands as argv, classed by a table in code, in a milestone M3d

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.12](../spec.md#712-host-commands-and-plans) (R-CMD-1, 2, 4–6, 8), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-4, 6, 8), [10.2](../spec.md#102-p1--all-in-one--scope-and-acceptance); [M3d design](../plans/p1-m3d-host-commands-design.md); [ADR-0031](0031-agent-security-baseline.md) (the baseline), [ADR-0039](0039-command-sandbox.md) (the sandbox), [ADR-0040](0040-plans-as-approvals.md) (plans)

## Context

The owner's disk filled to 100 %. Another assistant on the same machine
(OpenClaw) cleaned it from Telegram: it measured caches, ran
`go clean -cache` and `docker builder prune`, removed images on request
and built a tree of what takes the space. The owner sees this as an
everyday need of real users, on a local node and in the cloud, and wants
it to run on a schedule too. ganglion has no such tool.

That assistant's transcript (read on 2026-11-03) shows the risks. Its
`exec` runs every command as the user, and its approval list was empty.
On its own it tried `sudo -n rm -rf …` (it failed only for lack of
password-less sudo), and asked to "remove the images too" it ran
`docker rm $(docker ps -aq)` and deleted 19 stopped containers.

The owner's requirements: commands on the owner's behalf, locally and as
the service's user in the cloud; dangerous commands filtered **in code,
not only by an instruction to the model**; `sudo` never; reading can be
dangerous too (`env`); a short plain-words line on what a command will
do, because the user may not know what Docker is.

This is outside the current phase plan: M3c is next and does not include
it. The owner asked for it right after M3c.

## Options

### A. A shell string with an approval list (as OpenClaw)

The model sends a command line; a list of patterns decides what runs
without asking. Simple and flexible: pipes, globs, `$(…)` work.

### B. argv with no shell, classed by a table in code

The model sends a program and its arguments as a list; no shell parses
them. A table in Go code keyed by program, subcommand and flags puts
each command in a class: read (runs by itself), content, change, script
(need the owner's press), forbidden (never). Unknown programs are change.
Each entry carries a fixed consequence line in English, Russian and
Ukrainian.

### C. No commands; only built-in tools

Write each need as a Go tool: `host.stats`, a disk tree, a Docker API
client for prune, `go clean`. Nothing arbitrary runs.

## Comparison

| Criterion | A. Shell + patterns | B. argv + class table | C. Built-in tools only |
|---|---|---|---|
| Can a rule be walked around | Yes: `bash -c`, `$(…)`, `python -c`, `find -delete` defeat any pattern list | No parser to fool; scripts are a class of their own that needs a press | Nothing to walk around |
| Covers the owner's cases (disk tree, Go and Docker caches, a nightly check) | Yes | Yes | Only what we write, one tool per need |
| Other users' needs we did not foresee | Yes | Yes, through a press | No |
| Who decides what is dangerous | Patterns the model's text must match | The table, in code; the model never chooses the class | Code |
| Explains impact to a non-expert | Only the model's words | A fixed line per entry, not from the model | Per tool |
| Effort | Small | Medium: the table, its tests | Large and never finished |

## Decision

**B, plus `host.stats` from C.** A pattern list over a shell string
cannot be made safe, and the owner asked for rules in code. A table over
argv keeps the model out of the decision while still covering needs we
did not foresee, behind a press. `host.stats` (memory, load, disk, the
node's own folders) is written in Go because it must work in the cloud
even where the sandbox of ADR-0039 cannot be set up.

The work is a new milestone **M3d**, after M3c (owner, 2026-11-03),
about one weekend. P1's budget grows by that weekend; the cut list stays
in force.

## Consequences

- The agent can answer "where did the space go" and clean caches from
  Telegram, and scheduled tasks can do it unattended inside their grant
  (ADR-0040).
- Pipes and shell features are gone: a step that needs them is a script,
  shown in full and approved with a press.
- The class table is code we own and must keep current (Docker
  subcommands, new tools). A wrong entry towards read is a hole, so the
  table has tests per entry and moving an entry towards read is the
  owner's decision.
- Command output is external data and taints the session (R-POL-6).
- A long command runs in the background and resumes the conversation
  when done, reusing the resume path of ADR-0032.
- The node also accounts for its own files and cleans them by age or
  size (R-CMD-8); the claude CLI work directory is the first case.

## Revisit when

- Users keep needing a command family the table does not cover well
  (then add entries, or a dedicated tool such as a Docker API client).
- Tool placement (P2) moves commands to other nodes.
