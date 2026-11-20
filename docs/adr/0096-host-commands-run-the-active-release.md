# ADR-0096: Run a host command as the node's active release

- **Status:** Accepted; an image's binary not older than the active release runs the command itself, per [ADR-0102](0102-the-images-binary-runs-when-it-is-not-older-than-the-active-release.md)
- **Date:** 2026-11-18
- **Deciders:** Igor Artasevych
- **Spec:** [7.15 Updating the node](../spec.md#715-updating-the-node) (R-UPD-4,
  R-UPD-9)

## Context

A node updated with `/update` runs a release from `<data>/releases`; the
binary the image ships is never replaced (R-UPD-4,
[ADR-0081](0081-update-the-node-from-signed-releases-at-the-owners-command.md)).
Only `ganglion run` moves onto the active release, through the launcher.
Every other command an operator types on the host (`ganglion backup now`,
`ganglion secret set`, `ganglion update`, `ganglion mcp`, `ganglion
version`) still runs the image's binary.

Seen on the owner's node on 2026-11-17
([#252](https://github.com/igorrius/ganglion/issues/252)): after `/update
install v0.6.0`, `ganglion version` said `dev` and `ganglion backup now`
did not exist; it worked as `<data>/releases/v0.6.0/ganglion backup now`.
The guide writes these commands as `ganglion …`. A command added after the
image was built is missing, and an older one may read the database or the
settings differently from the node that runs on them.

Facts:

- The image runs the node as uid 10001; the guide tells the operator to
  run every host command as that user (`setpriv --reuid=10001 …`), and a
  shell from `railway ssh` starts as root.
- The node writes `<data>/releases` as uid 10001. No host command of the
  agent and no module can write it (R-UPD-4), but a process running as
  uid 10001 can.
- `Store.Verify` checks an installed release again: the signature over
  `SHA256SUMS`, the binary's sum and `release.json`.

## Options

### A. The image's binary hands the command to the active release

Before it parses the command line, a binary that is not the active
release looks for `<data>/releases/state.json` beside the database the
command names. When there is an active release, its binary belongs to the
user running the command, and it passes `Store.Verify`, the process
executes it with the same arguments. Otherwise the command runs as it is,
with one line on stderr saying why.

### B. The guide names the release's path

No code. The guide tells the operator to run
`<data>/releases/<version>/ganglion …` on an updated node, and to look up
the version first.

### C. A link on the `PATH` to the active release

`/update` keeps a link `<data>/bin/ganglion` to the active binary and the
image puts `<data>/bin` before `/usr/local/bin` on the `PATH`.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| The guide's commands work as written | Yes | No | Yes |
| The release is checked before it runs | Yes, each time | No | No |
| root runs a file the node's user can change | No: refused | When the operator does it | Yes, for every command |
| Works outside the image (a package, a Pi) | Yes | Yes | Only where `PATH` is set |
| Built here | A dispatch step before parsing | Nothing | A link, a `PATH` and its upkeep |

## Decision

**A.** Every command but `run` starts with a dispatch step:

- The database is the one the command line names (`--db`), else
  `GANGLION_DB`, else `ganglion.db`, as each command reads it. A node
  with no `state.json` beside it, or no active release, runs the
  command as it is: that is a node that never updated.
- When this process is the active release's binary, it runs on.
- When the binary's owner is not the user running the command, it runs
  on as it is and says on stderr which release the node runs and to run
  the command as the node's user.
- When the release does not pass `Store.Verify`, it runs on as it is and
  says so.
- Otherwise it executes the active binary with the same arguments and
  environment. The state is read only: a dispatch counts no attempt and
  falls back nowhere; that stays the launcher's (R-UPD-5).

`run` keeps its launcher.

## Security

- **Trust boundaries:** the image's binary and the files under
  `<data>/releases`, which the node's user can write.
- **What an attacker controls:** a node broken into as uid 10001 can
  change the files of a release and `state.json`.
- **Abuse cases and what stops them:**
  - *root runs a binary the node's user planted.* A dispatch never
    executes a file whose owner is not the user running the command;
    root gets the image's binary and a line telling it to use the node's
    user.
  - *A changed release runs.* `Store.Verify` checks the signature and the
    sums before each dispatch; a changed file is refused and the command
    runs as the image's binary. A process that is already uid 10001 can
    swap the file after the check, but it gains nothing: the command runs
    as the same user.
  - *`state.json` names a path outside the store.* The version must parse
    as a core version (`ParseVersion`) before its path is built.

## Consequences

- `ganglion …` on the host runs the version the node runs, so the guide's
  commands work as written after `/update`.
- A command run as root on an updated node warns and runs the older
  binary. The guide already tells the operator to use the node's user.
- A broken or refused release leaves the host's commands on the image's
  binary, which is the floor the node falls back to as well.

## Revisit when

A node is installed some way where the operator's user is not the node's
user by design.
