# ADR-0057: Let commands list the folders the sandbox splits

- **Status:** Accepted (2026-11-08)
- **Date:** 2026-11-08
- **Deciders:** Igor Artasevych
- **Spec:** [7.12](../spec.md#712-host-commands-and-plans) (R-CMD-3); refines [ADR-0039](0039-command-sandbox.md), which is accepted and not rewritten: this ADR records the change; task 11a of the [M3d commands plan](../plans/p1-m3d-commands.md)

## Context

Landlock grants whole trees only. To deny `~/.ssh` inside an allowed
home, the sandbox of ADR-0039 splits every folder that holds a denied
path into its other entries and grants those, never the folder itself
(`sandbox.Grants`). A split folder can therefore not be listed: opening
it for reading needs `READ_DIR` on it, and nothing grants that.

On the owner's machine `/`, `/home` and `~` are split, since they hold
`~/.ssh`, `~/.gnupg`, the cloud CLI configs and the files that run at
login. The deny list keeps paths that do not exist yet, so home is split
on every machine. What fails because of it, found while building task 11:

- `ls ~`, `ls /` and `ls /home` fail with "Permission denied";
- `du -sh ~` fails, while `du -sh ~/projects` works;
- `ls /tmp` fails while the node's data folder or a self-test folder is
  there;
- `find ~ -maxdepth 1`, `tree ~` and a file manager's view of home fail
  the same way.

The agent cannot answer "what takes space in my home" in one command; it
has to guess the names of home's entries and ask about each.

Landlock does not govern `stat`: a command can already learn the size,
owner and times of `~/.ssh/id_ed25519` when it knows the name. What the
split hides is the names.

## Options

### A. Keep it: split folders cannot be listed

The sandbox stays as ADR-0039 built it. Names inside denied folders stay
hidden, and every command that lists or walks `/`, `/home` or `~` fails.

### B. `READ_DIR` on each split folder

Each split folder gets a rule with `LANDLOCK_ACCESS_FS_READ_DIR` and no
other right. Landlock applies a rule to the folder and its whole tree, so
every folder beneath, denied ones included, can be listed: the names in
`~/.ssh` become visible (that it holds `id_ed25519`), never the contents.
Reading, writing, executing, truncating, creating, removing and renaming
stay where they were.

### C. A Go tool that lists a split folder's sizes outside the sandbox

A tool beside `host.stats` that walks a folder in the node and reports
what takes space, leaving out the denied paths. It duplicates `du`, `ls`
and `find`, and every command the agent tries on its own would still
fail.

## Comparison

| Criterion | A. Keep | B. `READ_DIR` | C. A Go tool |
|---|---|---|---|
| `ls ~`, `ls /`, `du -sh ~`, `find ~` | Fail | Work | Fail; the tool answers sizes only |
| What reaches the model beyond today | Nothing | The names inside denied folders | Nothing |
| Contents of denied files | Denied | Denied: no read, write, execute or truncate right is granted | Denied |
| Effort | None | One right on one rule, a setting, tests | A new tool with its own walk, limits, texts and tests |
| Fits "commands, not tools for each question" (ADR-0038) | Yes | Yes | No |

## Decision

**B by default, A through `GANGLION_SANDBOX_HIDE_NAMES=on`** (flag
`--sandbox-hide-names`; owner, 2026-11-08). The questions commands are
for, "what is in my home" and "what takes space", need a listing of the
folders the sandbox splits, and B gives it with one right and no new
code path. What it gives away is names, and the sizes and times that
`stat` already showed for a known name; contents stay denied by the same
rules as before. An owner who would rather keep the names hidden turns
it off.

A list grant is `READ_DIR` only, on a folder that is split and not itself
denied. A denied folder is never granted; its names are reached through
its parent's grant. The self-test passes the setting, and still fails to
read a denied file.

## Security

- **Trust boundaries:** none new. The sandbox stays the boundary between
  a command and the node's data and the owner's secret stores; this
  widens what a command can see across it from nothing to names.
- **What an attacker controls:** the command, through a model in a
  tainted session or a skill script the owner approved.
- **Abuse cases and what stops them:**
  - Reading a denied file through the listed folder: the rule grants
    `READ_DIR` alone, so `READ_FILE` is still missing; tested with the real
    Landlock, and by the self-test at every start.
  - Writing into a split folder (a file beside `~/.ssh`, a new
    `~/.bashrc`): `MAKE_REG`, `WRITE_FILE` and the rest are not granted;
    tested.
  - Executing a denied file: `EXECUTE` is not granted.
  - A secret in a file name (a token used as a name): it reaches the
    model as text a command printed, which is outside content, and the
    node's own secret values are hidden from command output as before.
    An owner who keeps such names sets `GANGLION_SANDBOX_HIDE_NAMES=on`.

## Consequences

- The file names in `~/.ssh`, `~/.gnupg`, the cloud CLI configs, the data
  folder and the workspace can reach the model, and the provider and the
  journal with it.
- A press still guards every content command (ADR-0038, R-CMD-2): `cat`, `head`
  and the scripts that could read what is granted ask first.
- The self-test still tries to read a denied file and fails, now with the list grants in
  place, so a kernel where `READ_DIR` reached further would turn commands
  off.
- A folder created later directly in a split folder stays unreadable, as
  before, but its names can be listed.
- `ls ~`, `du -sh ~` and `ls /` work; `du` counts the sizes inside denied
  folders too.

## Revisit when

- A file name in a secret store turns out to carry a secret in practice
  (then hide names by default).
- Landlock gains a right that lists a folder without its tree, or the
  sandbox moves to bind mounts (ADR-0039 option B): grant listing of the
  split folders alone.
