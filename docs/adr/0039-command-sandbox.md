# ADR-0039: Run host commands and skill scripts in a sandbox that fails closed

- **Status:** Accepted (2026-11-08)
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.12](../spec.md#712-host-commands-and-plans) (R-CMD-3), [11](../spec.md#11-open-decisions) ("Sandbox for scripts and MCP modules"), [7.8](../spec.md#78-security-and-operations-mvp); [M3d design](../plans/p1-m3d-host-commands-design.md) section 5; [ADR-0038](0038-host-commands-as-argv-with-classes-in-code.md), [ADR-0031](0031-agent-security-baseline.md); decision D-5 in the [phase plan](../plans/p1-all-in-one.md#decisions-to-take-during-p1)

## Context

`host.exec` (ADR-0038) runs programs as the node's Unix user. A process
of that user can, unless stopped:

- gain root through a set-uid program (`sudo`, `pkexec`);
- read the node's secrets: its environment through
  `/proc/<node pid>/environ`, the database, `*_FILE` secrets, `.env`;
- on the owner's machine, read `~/.ssh`, `~/.gnupg` and other secret
  stores.

What a command prints goes to the model provider and the journal, and a
tainted session may try to carry it out. The owner: `sudo` is forbidden;
`env` and similar reads too.

The press of ADR-0040 is the boundary for what an exact command shows;
the sandbox is what holds for everything else, including scripts the
owner approved without reading closely. Commands that would leave the
sandbox through a daemon (`docker run`, `systemd-run`, crontab) are
forbidden by the class table, not by the sandbox.

Where it must run: the owner's Linux machine (kernel 7.0, Landlock and
Yama on, `ptrace_scope=1`, unprivileged user namespaces on), a Railway
container and a Pi; their kernel features differ and are not yet
checked. Principle 8: no resident helper process. M3c's skill scripts
(D-5: subprocess, environment allowlist, timeout) should move onto the
same sandbox.

## Options

### A. In-process: `no_new_privs`, a clean environment, Landlock, a PID namespace

The node sets `no_new_privs` (on itself at start, or in a small re-exec
helper before `exec`), passes a fixed environment allowlist, restricts
the file system with Landlock, and starts the command in a new PID
namespace (through an unprivileged user namespace) so the node's process
is not visible. Pure Go plus one library (`go-landlock`) or raw syscalls.

### B. An external sandbox binary: bubblewrap or nsjail

The node starts commands through `bwrap` or `nsjail` with a fixed
profile: namespaces, bind mounts that hide the denied paths, seccomp.
Mature and expressive (it can cover a denied folder inside an allowed
one), but one more binary to ship, and it needs user namespaces too.

### C. A separate Unix user

Commands run as a dedicated unprivileged uid with its own home, so the
node's files and process are another user's. The strongest isolation of
files, but it needs root to set up and to switch users, and it breaks
"on the owner's behalf": the owner's caches belong to the owner's user.

### D. No sandbox: the D-5 baseline only

A clean environment and a timeout. `/proc/<node pid>/environ`, the
database and `sudo` stay open.

## Comparison

| Criterion | A. In-process | B. bwrap / nsjail | C. Separate uid | D. Baseline |
|---|---|---|---|---|
| Blocks root through set-uid | Yes (`no_new_privs`) | Yes | Yes | No |
| Hides the node's secrets and process | Yes, with a PID namespace and Landlock | Yes | Yes | No |
| Hides a folder inside an allowed one (`~/.ssh` in `~`) | Only by granting each sibling | Yes (bind mounts) | Yes (permissions) | No |
| Acts on the owner's files as the owner | Yes | Yes | No | Yes |
| Extra binary or setup | None or one library | A binary per platform | Root to set up | None |
| Works in a container without user namespaces | `no_new_privs` and Landlock yes; PID namespace no → off | No → off | Needs root | Yes, unsafe |

## Decision

**A without the PID namespace, failing closed** (owner, 2026-11-08). A
PID namespace would leave `ps`, `top` and `kill` blind to the machine,
the very questions commands are for, and needs user namespaces that
containers often lack. The node instead marks itself not dumpable (it
already does, ADR-0042): its `/proc/<pid>/environ` and memory then belong
to root, and a process of the same user cannot read them. `ps` still
lists the node with its command line, which holds no secret. The rest of
option A stands as written, to be confirmed by a spike in M3d task 3.20
on the owner's machine, Railway and a Pi before this ADR is accepted. It
keeps
"on the owner's behalf", adds no binary and no resident process, and its
parts are what the kernel already offers. B stays the fallback if the
spike shows A cannot hide the denied paths well enough.

Fail closed: if the node cannot set up `no_new_privs`, the clean
environment, a node that is not dumpable and the denied paths, or if it
runs as root (uid 0, where `no_new_privs` takes nothing away),
`host.exec` is not offered to the model and the node reports why.
`host.stats` does not depend on the sandbox. The M4 container image sets
an unprivileged `USER`.

The spec's open decision "Sandbox for scripts and MCP modules" is closed
by this ADR once it is accepted; MCP modules (P3) revisit it.

## Spike

Task 3.20's [spike](../research/2026-11-08-command-sandbox-spike.md) ran on
the owner's machine (Landlock ABI 7) and in a Docker container with the
default seccomp profile (ABI 7): Landlock, `no_new_privs`, a seccomp
filter and a not-dumpable node all hold. Railway and a Pi are not yet
measured; the owner accepted the decision on these two (2026-11-08). On
either, the node's checks at start decide: it says in `/status` and its
log whether commands are on, and why not.

## Refined by the spike and the review (2026-11-08)

The build of task 3.20 and a review that tried to break it settled what
option A needs beyond the paragraph above:

- **The helper is the process's own image** (`/proc/self/exe`), and the
  node's binary is denied: a command that replaced the file at the
  binary's path would otherwise run as the next helper, before any rule.
- **The deny list is built again for each command** and keeps paths that
  do not exist yet, so a secret store or data file that appears after
  start is denied, and a store that became a symlink is followed. A
  folder created later directly in a split folder is denied as well
  (fail closed).
- **Unix sockets.** Landlock reaches socket files only from ABI 9; below
  it a command could ask gpg-agent or the keyring (over D-Bus) for
  secrets without touching a denied file. Below ABI 9 the helper refuses
  `socket(AF_UNIX, …)` and `io_uring_setup` with a seccomp filter, and the
  self-test checks it (owner, 2026-11-08: refuse all rather than allow
  with the risk). The price: `docker ps`, `systemctl --user` and `lpstat`
  do not work from commands there. Abstract sockets are scoped to the
  sandbox from ABI 6 as well. `socketpair` stays.
- **Signals are not scoped** (owner, 2026-11-08): `kill` is a change
  command behind a press and must reach the owner's processes; a command
  can therefore stop the node, which loses no secret.
- **Files that run at the owner's next login** (shell start files,
  `~/.config/systemd`, `~/.local/share/systemd`, autostart,
  `environment.d`) are denied with the secret stores and shell histories:
  writing them would leave the sandbox later.
- **The data folder** is denied whole when the database has a folder of
  its own; when it lies in the working folder, home or `/`, the database
  and its journal files are.
- **`.env`, `.env.*` and `.envrc`** are found under the command's folder
  and home to depth 3 at each command, symlinks resolved to their
  targets; a deeper one stays readable (the spike's note).

## Consequences

- Skill scripts (3.7) move onto this sandbox in M3d (task 3.26).
- The default deny list of secret paths (`~/.ssh`, `~/.gnupg`, cloud CLI
  configs, the node's data directory, `.env` and `.env.*` files in any
  folder) is ours to keep; the
  owner can extend it.
- Landlock's grant-only model means the allowed tree is enumerated for
  each command; something created later directly in a split folder is
  denied, something under a granted folder follows that folder.
- Path rules do not reach a second name of a denied file: an existing
  hard link, a bind mount or a filesystem snapshot elsewhere stays
  readable. Landlock keeps a command from making new links into denied
  places or new mounts.
- Commands are Linux only; on another OS the tool is off.
- A user in the `docker` group can reach root through Docker; the class
  table forbids the Docker subcommands that start containers, and
  scripts need a press that says they run with the user's rights.

## Revisit when

- Railway or a Pi, once measured with `tools/sandboxprobe`, lacks Landlock
  or a seccomp filter (then commands are off there, or B).
- MCP modules (P3) need the same sandbox with network rules.
- A node runs on a kernel with Landlock ABI 9: build socket rules on
  `LANDLOCK_ACCESS_FS_RESOLVE_UNIX` there (deny the sockets under denied
  paths and the owner's agent and keyring sockets, allow the rest), test
  it on such a kernel, and drop the seccomp refusal on it.
