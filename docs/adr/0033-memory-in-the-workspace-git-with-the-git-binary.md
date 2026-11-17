# ADR-0033: Keep memory as Markdown files in the workspace's git repository, driven by the `git` binary

- **Status:** Accepted; the file layout (one fact per file) superseded by [ADR-0071](0071-memory-topics-whole-in-the-prompt.md)
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.7](../spec.md#77-memory) (R-MEM-3–5), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-7), principle 8 (a node fits in 128 MB); decision D-7 of the [phase plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-all-in-one.md); design section 5.4 ([M3b and M3c design](../design/p1-m3b-m3c-design.md#54-memory-spec-task-35)); follows the M3b note of [ADR-0024](0024-persona-as-workspace-files.md)

## Context

M3b gives the agent long-term memory (R-MEM-5): facts about the owner,
their home and their preferences, written by the agent itself.
[ADR-0024](0024-persona-as-workspace-files.md) put the persona in
`<workspace>/persona/*.md` and planned that "the workspace becomes a git
repository with memory beside `persona/`" in M3b. This ADR is that
decision.

What memory needs:
- **Undo.** A bad entry (a mistake, an injection that got through) is
  taken back with a revert; every change is a commit (R-MEM-3).
- **Readable by the owner.** Plain Markdown the owner can open, edit and
  grep, with where each fact came from (turn, taint, date).
- **Small.** A node runs on a Pi in 128 MB (principle 8). Memory writes
  are rare: a few a day.
- **No new trust.** The repository must not pick up the user's global git
  configuration (hooks, signing, a `GIT_DIR` from the environment).

Decision D-7 asks: the `git` binary or go-git, a pure-Go implementation.

Measurements on the development machine (Intel Core Ultra 7 265U,
Linux, Go 1.27.3, go-git v5.19.3, git 2.53.0; builds with
`CGO_ENABLED=0 -trimpath -ldflags='-s -w'`; throwaway modules in a
scratch directory, nothing added to `go.mod`):

| Measure | `git` binary | go-git v5.19.3 |
|---|---|---|
| ganglion binary, blank import only (linker keeps init-reachable code) | 24.96 MB | 27.51 MB (+2.55 MB, a lower bound) |
| Standalone program doing init, add and commit | 1.90 MB | 7.44 MB (+5.5 MB; arm64: 1.90 → 6.95 MB) |
| Peak RSS of the program, 50 commits | 6.1 MB, of which the git children ~5–6 MB, transient | 14.3 MB, in the node's process |
| Peak RSS, 300 commits | 7.8 MB | 19.4 MB, growing with the work tree |
| Commit latency, 50 commits (add + commit) | 12.5 ms | 1.9 ms |
| Commit latency, 300 commits | 15.5 ms | 4.2 ms, growing with the work tree |
| `gitrepo.Repo.Commit` as built (add, status, commit, rev-parse) | 22 ms (`go test -bench Commit ./gitrepo`) | — |

Estimate for a Raspberry Pi 4: about five times slower per process
start and per file operation, so roughly 100–200 ms per memory commit
with the binary and 10–25 ms with go-git. At a few writes a day the
difference is not visible; the turn around it takes seconds. The `git`
package of Raspberry Pi OS (Debian) takes about 40 MB of disk with its
dependencies and is often installed already; go-git costs nothing on
disk but lives in the node's memory for the life of the process.

## Options

### A. The `git` binary

Run `git` as a child process for each change: `add`, `status`, `commit`,
`revert`. The identity is set per command (`-c user.name=ganglion -c
user.email=ganglion@localhost`), the global and system configuration
are switched off (`GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM`),
git's own environment variables are dropped, hooks and signing are off.
A host without `git` runs without memory.

### B. go-git

Link go-git and drive the repository in-process. No external program;
the repository format is the same.

### C. No git: a history table in SQLite

Keep facts as files and their history as rows. Undo is our own code;
the owner cannot use any git tool on it.

## Comparison

| Criterion | A. git binary | B. go-git | C. SQLite history |
|---|---|---|---|
| Resident memory of the node (principle 8) | Unchanged; ~6 MB only while a commit runs | +12–17 MB measured, more with a larger tree | Unchanged |
| Binary size | Unchanged | +2.5–5.5 MB | Unchanged |
| Commit latency on a Pi | ~100–200 ms (estimate) | ~10–25 ms (estimate) | ~1 ms |
| Behaviour matches what the owner sees with `git log` / `git revert` | Exactly | Close; some commands missing (no `revert` in v5) | No |
| New dependency in `go.mod` (ADR needed, ≥ 2 weeks old) | None | go-git and ~20 modules | None |
| Works on a host without git | No: memory is off, with a warning | Yes | Yes |
| Isolation from the user's git configuration | Must be set per command (done in `gitrepo`) | By construction | n/a |

## Decision

**A, the `git` binary.** The node's memory is the scarce resource on a
Pi, and memory writes are rare: a short-lived child process costs
nothing while idle, where go-git stays resident and grows with the tree.
The owner reverts with the same tool the agent commits with, and
`go.mod` stays as it is.

The rules, in package `gitrepo`:
- `Open` runs `git init` when the directory itself has no `.git` (a
  checkout the workspace sits in does not count). The author is
  `ganglion <ganglion@localhost>`, set per command; the user's global
  configuration is never read or written.
- `Commit` takes only the paths it is given (other changes stay out),
  refuses a path that is absolute, climbs out with `..` or points into
  `.git`, and returns the current commit when there is nothing to commit,
  so a rerun turn is not an error.
- Memory lives in `memory/<slug>.md` with YAML frontmatter (title,
  created, source turn, tainted, checked, reason); quarantined entries in
  `memory/quarantine/` (R-POL-7, [ADR-0034](0034-clean-context-guard.md)).
- **No git, no memory.** When `git` is missing, the node starts without
  the memory tools and without the known-facts block, and logs a warning
  that says why. Any other failure to open the workspace stops the start.

## Consequences

- Every memory change is a commit with the turn in its message; the owner
  undoes one with `git revert`.
- The workspace (`GANGLION_WORKSPACE`) must be writable, and the
  container image needs `git`.
- Writes are serialised by a lock in `gitrepo` and in `memory`; the guard
  runs inside the memory lock, so a slow check delays a second write.
- A git that is killed half-way leaves `.git/index.lock`, and every later
  write fails. So git runs apart from the turn's cancellation, each
  command under a 10-second timeout; a git killed at that timeout has its
  lock removed, and `Open` removes a stale one left before the start
  (this process is the only writer). A crash between writing a fact's
  file and its commit is repaired by the rerun of the same write, which
  commits the file it finds uncommitted (ruling R37).
- The persona files sit in the same repository; committing persona
  changes is task 9 (`persona.propose`).

## Revisit when

- Memory writes become frequent (the nightly consolidation of R-MEM-1 in
  M7 writes many at once) and the per-commit cost shows up.
- A deployment target without `git` matters (a minimal container).
- go-git gains `revert` and its resident cost drops.
