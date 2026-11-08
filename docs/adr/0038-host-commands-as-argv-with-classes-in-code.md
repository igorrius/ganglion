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

## Refined by the build (2026-11-08)

The plan for tasks 3.21–3.27 ([p1-m3d-commands.md](../plans/p1-m3d-commands.md))
settled what the design left to the builder:

- **Scripts are inline only.** `bash -c`, `sh -c`, `python3 -c`,
  `node -e`, `perl -e`, and `awk` and `sed` with their program in argv are
  script class: the press shows the whole script. An interpreter given a
  file, a module or its input (`bash x.sh`, `python3 -m`, `awk -f`) is
  forbidden, with a line saying to write the script out. Without this,
  "the press shows the whole script" would not hold.
- **A command is argv plus its folder.** Plans and grants approve
  `{argv, dir}`: `rm -r build` in one folder is not `rm -r build` in
  another.
- **`ps` and `top` are read only with name-only columns.** `ps` with only
  `-e`, `-A`, `--no-headers`, `-p`, `-u`, `-U`, `--sort` and `-o` over a
  list of safe fields (`comm`, never `args`) is read. Anything else
  (`aux`, `-f`, `e`) is content, since full command lines and environments
  can hold secrets. `top` is read only as `-b` with `-n`.
- **Docker runs only listed subcommands.** Any other is forbidden:
  `docker run`, `exec`, `create`, `start`, `restart` and `compose up` start
  processes outside the sandbox, and an unknown one might.
- **Docker does not work from commands below Landlock ABI 9.** The CLI
  talks to `/var/run/docker.sock`, a Unix socket the seccomp filter of
  [ADR-0039](0039-command-sandbox.md) refuses (owner, 2026-11-08). On a
  machine with ABI 7 Docker commands are classed, asked for and approved as
  designed, and then fail with "cannot connect to the Docker daemon". The
  scripted acceptance proves Docker's classes and grants with a fake runner;
  the live one uses `go clean -cache` and folders the check makes in place
  of `docker image prune`. Changing this needs a new decision on ADR-0039.
- **The kill switch is the cell `tools.host_exec`.** It is defined at
  start; "off" stops new commands, and commands already running finish. An
  admin's `/commands on|off` sets it. `cells.set` refuses every `tools.*`
  cell, so the agent cannot widen its own rights. A global switch for every
  tool stays as R-POL-5 has it, for later.
- **A long command in a scheduled run resumes the task under its own
  grant.** The `command.done` event of a task's conversation reloads the
  task and its grant, as the run did. Without a grant it would resume as an
  ordinary turn.
- **The node's places on disk are one list in `config`:** the database,
  logs, workspace, modules, language files, the claude CLI work folder
  (moved under the data folder) and its leftovers in the temp folder.
  `host.stats` reports them, and the janitor removes what is past its age:
  24 hours for the CLI work folder and the temp leftovers.

How a long command works:

- one `host_jobs` row per call key;
- a `command.done` event put through the queue, like the expirer's
  `tool.result`;
- the agent's `State.Commands`, which keeps the plan open while a command
  runs.

What the journal stores of a command: argv and folder in its arguments;
exit code, duration and output size in the head of its result.
