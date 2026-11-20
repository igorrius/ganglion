# ADR-0102: Run the image's binary when it is not older than the active release

- **Status:** Accepted
- **Date:** 2026-11-20
- **Deciders:** Igor Artasevych (the owner)
- **Spec:** [R-UPD-4](../spec.md#715-updating-the-node),
  [R-UPD-9](../spec.md#715-updating-the-node); supersedes the rule of
  [ADR-0081](0081-update-the-node-from-signed-releases-at-the-owners-command.md)
  that the launcher always moves onto the active release, and the same
  rule in [ADR-0096](0096-host-commands-run-the-active-release.md)

## Context

[ADR-0081](0081-update-the-node-from-signed-releases-at-the-owners-command.md)
made `ganglion run` the launcher: at start it reads
`<data>/releases/state.json` and, when it names a release other than the
running binary, verifies that release and `exec`s it. The binary of the
image is the floor to fall back to.
[ADR-0096](0096-host-commands-run-the-active-release.md) does the same for
host commands.

Neither says what happens when the image is newer than the stored
release. In the live check of #261 (2026-11-19) a node had installed
v0.6.0 by `/update`. An image built from `master` was deployed over it.
The launcher saw an active release that was not this binary and ran
v0.6.0: the new tools of the image never reached the model, the logs kept
saying v0.6.0, and nothing said that the deployed image was ignored
(#279). The only way out was to cut a release and install it with
`/update`.

Facts:

- A deploy of an image is an operator's act outside the chat; nothing on
  the node can tell a deploy from a restart of the same image except the
  binary itself.
- The binary knows its version (`buildinfo`). A release build reports
  `vX.Y.Z`; a build from a checkout reports `git describe`
  (`v0.7.7-3-gabc`, perhaps `-dirty`) or `dev`.
- `/update` refuses to move a build that has no release version
  (ADR-0081), so a node that runs such a build has no active release to
  go back to through the chat anyway.
- `state.json` and `<data>/releases` are the node's own; a host command
  and a module cannot write them (R-UPD-4).

## Options

### A. The image wins when it is not older, and the stored releases are dropped

The launcher compares the image's version with the active release's. When
the image is not older it runs as it is, writes a state with no active and
no previous release, and removes the release folders. A build without a
release version counts as newer: someone deployed it on purpose. A
`git describe` build counts as its tag plus commits: newer than or equal
to a release at its tag or below, older than one above it. An older image
still moves onto the release, and the launcher says so in one line.

### B. The image wins when it is not older, decided again at every start

The same comparison, but the state is left as it is: every start decides
afresh. `/update`, `/status`, the watch and host commands keep reading an
active release that does not run.

### C. Any new image wins

The install records a fingerprint of the image's binary; a start with a
different one is a redeploy and runs the image, whatever its version. An
older image deployed on purpose wins too.

### D. Keep the rule and warn

The launcher keeps moving onto the release and logs that the image was
ignored. The operator removes `state.json` by hand to run the image.

## Comparison

| Criterion | A. Not older, drop | B. Not older, every start | C. Any new image | D. Warn only |
|---|---|---|---|---|
| A newer image runs after a deploy | Yes | Yes | Yes | No |
| An older image over a newer release | The release runs | The release runs | The image runs | The release runs |
| State matches what runs | Yes | No | Yes | Yes |
| Host commands and `/update rollback` | Run the image; nothing to roll back to | Need the comparison everywhere | Run the image | Unchanged |
| A non-reproducible rebuild of the same image | Same version: the image runs, it is the same code | Same | Drops a newer installed release | Unchanged |
| Code | `Decide`, the launcher, the dispatcher | `Decide`, the dispatcher, the watch, `/update` | A fingerprint in the state, `Decide` | One line |

## Decision

**A.** A deploy is how the operator says "run this", and the version is
the only thing the binary knows that orders two builds. An image that is
not older than the release carries everything the release has, so the
stored releases have nothing left to give: dropping them keeps the state
equal to what runs, and `/update`, the watch and host commands need no
second rule. An older image is the one case where the version and the
deploy disagree; the release keeps running, the launcher says so at every
start, and the operator who wants the older image removes
`<data>/releases/state.json` (or the folder).

Details:

- The comparison: a release version is compared as semver; a
  `git describe` version by its tag, and when the tag is the release's
  own the image is newer; any other version string, `dev` included, is
  newer.
- When the image wins the launcher writes `{active: "", previous: ""}`,
  keeps `confirmed`, drops a pending release and an untold fall back, and
  prunes the release folders, under the store's lock. The watch then sees
  a version that is not the confirmed one and tells the admins which
  version runs, as after a rollback.
- The dispatcher of host commands does the same comparison and runs the
  image's binary when it wins; it only reads the state, as before.
- The release's own process, `exec`'d by the launcher, is the active
  release and does not compare: it runs.

## Security

- **Trust boundaries.** None new: the image's binary is the trust root of
  ADR-0081, deployed by the operator.
- **What an attacker controls.** One who runs code as the node's user can
  write `state.json`. They cannot change the image's binary or its
  version.
- **Abuse cases:**
  - *A downgrade through the state.* The comparison uses the image's
    version, fixed in the binary, and the release's, which the launcher
    verifies before running it. Writing an older version into the state
    makes the image win; writing a newer one needs a signed release of
    that version on disk.
  - *Losing an untold fall back.* Dropped only when the image wins, which
    replaces the release the fall back was about; the admins are told
    which version runs.

## Consequences

- A redeploy of a newer image runs it, without a release and `/update`.
- The release folders and their `before.db` copies go when the image
  wins; `/update rollback` has nothing to go back to until the next
  install.
- An older image deployed over a newer release does not run; the log says
  so at every start. To run it, remove `state.json`.
- A node whose image is a build without a release version cannot use
  `/update` (as before), and a stored release no longer overrides it.

## Revisit when

An operator needs to run an older image over a newer release often enough
that removing `state.json` by hand is a burden, or the image starts to
carry a version that does not order with the releases'.
