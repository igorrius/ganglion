# ADR-0081: Update the node from the project's signed releases, at the owner's command

- **Status:** Accepted
- **Date:** 2026-11-13
- **Deciders:** Igor Artasevych
- **Spec:** [7.15](../spec.md#715-updating-the-node) (R-UPD-1…7), [7.8](../spec.md#78-security-and-operations-mvp), [7.14](../spec.md#714-node-configuration) (R-CFG-6), [9.8](../spec.md#98-self-modification-levels), [11](../spec.md#11-open-decisions); [ADR-0041](0041-module-install-update-and-trust-boundary.md), [ADR-0046](0046-a-restart-is-not-a-failed-attempt.md), [ADR-0039](0039-command-sandbox.md), [ADR-0016](0016-sqlite-migrations.md)

Marks: **owner** means the owner decided it. The owner chose the direction
and, on 2026-11-13, read and accepted the details as written.

## Context

The owner's node runs on Railway on the production bot, and the owner drives
development through it. A merge to `master` changes nothing there: the
service is not tied to the repository, and every deploy so far was
`railway up` from a developer's machine, which builds the 1.2 GB image
(the Rust toolchain included) for several minutes.

On 2026-11-13 the secret store was merged and the owner expected the node to
have it. It did not, and nothing said so. There is no way to tell which
commit runs beyond `/status`, and no way to move the node to a newer one
without the repository, the Railway CLI and a login.

What exists and bears on a decision:

- **Modules already update this way** ([ADR-0041](0041-module-install-update-and-trust-boundary.md)):
  a tag, a release built by CI with `SHA256SUMS`, a fetch from GitHub's
  hosts only, checks, a self-test, the previous version kept. Its
  signatures were left to P3.
- **`/restart`** ends the node with exit code 75 and the supervisor starts
  it again (R-CFG-6, [ADR-0046](0046-a-restart-is-not-a-failed-attempt.md)).
- **The node's binary is the trust root.** A module runs under a ceiling
  the core embeds; nothing stands above the core.
- **The agent never rebuilds the core** (spec 9.8, level 5).
- The project has no core tag yet; the version is `git describe`.
- The network of P2 has nodes on other machines (a Pi at home). Whatever
  updates the Railway node should update those.

Constraints: one owner, no operations team; the node is in use all day, so
a restart is the owner's decision; the image's environment (Rust, the
claude CLI, the entrypoint) changes far more rarely than the Go code.

## Options

### A. Deploy on every push to `master`

Tie the Railway service to the repository; a merge builds and deploys.

### B. An image per tag, switched on the platform

A core tag makes CI build the image and push it to a registry. The Railway
service runs an image by tag; an update is a change of the tag, by hand in
the dashboard or with the CLI.

### C. A command in the chat that asks the platform to redeploy

`/update` calls Railway's API with a token the node holds; Railway builds
or pulls and restarts the service.

### D. The node replaces its own binary from a signed release

A core tag makes CI build the binary for each platform, `SHA256SUMS` and a
signature over it. `/update` shows the running and the newest version; on
the owner's command the node fetches the release, verifies it, stores it on
the data volume beside the previous one and restarts into it. The binary
in the image stays as the floor to fall back to.

## Comparison

| Criterion | A. Every push | B. Image by tag | C. Platform API | D. Signed binary |
|---|---|---|---|---|
| Who decides when the node restarts | Whoever merges | The owner, outside the chat | The owner, in the chat | The owner, in the chat |
| Works off Railway (a Pi, a VPS) | No | With a registry and a puller per machine | No | Yes, the same code |
| What the node must hold | Nothing | Nothing | A Railway token that can deploy the project | The project's public key |
| Time to update | A full image build | An image pull | A build or a pull | One file of tens of MB |
| Falls back when the new version does not start | The platform keeps the old deploy only if the health check is set up | By hand | By hand | By itself, to the previous binary |
| Updates the environment (Rust, the claude CLI) | Yes | Yes | Yes | No: that stays an image rebuild |
| A compromised release channel | Runs at the next push | Runs when the tag is switched | Runs at the command | Refused without the signature |
| Fit with what exists | New | New: a registry | New: the platform's API | ADR-0041's path, `/restart` |
| Owner's view | "There must not be one" (owner) | Not chosen | Not chosen | Chosen (owner) |

## Decision

**D** (owner): the node updates itself from the project's signed releases,
and only at the owner's command. It is the one option that is the same on
every machine a node will run on, and it keeps the decision to restart with
the owner. The details below were the author's proposal, accepted by the
owner.

**Releases.**

- A core release is a tag `vX.Y.Z` on a commit of `master`. Module tags
  (`html2md/v…`, ADR-0041) stay apart.
- CI builds `ganglion-linux-amd64` and `ganglion-linux-arm64` (no cgo: the
  SQLite driver is Go), writes `SHA256SUMS`, signs it, and attaches the
  four files and a small `release.json` to the GitHub release.
- `release.json` says what the binary needs of its surroundings: `env`, a
  number that rises when the image's environment must change with the code
  (a new Rust target, a new entrypoint). The image carries its own `env`
  number.
- **The signature is Ed25519** over `SHA256SUMS`, verified with Go's
  standard library against public keys embedded in the binary. The private
  key is a secret of the release workflow. The binary embeds a list of
  keys, so a new key is introduced by a release signed with an old one.
  Sigstore's keyless signing was the alternative: no key to keep, but a
  client library and its dependency tree inside the trust root.

**The command.** For an admin, in their private chat, with no model:

| Command | Does |
|---|---|
| `/update` | Shows the running version and the newest release that fits this machine, with a link to its page |
| `/update install vX.Y.Z` | Fetches, verifies and stores that release, then restarts into it; naming the version is the confirmation |
| `/update rollback` | Restarts into the previous version |

`ganglion update` on the host does the same without a chat.

The node asks GitHub for the newest release once a day and tells the
admins when there is one. It never installs by itself. `update.check: off`
in `ganglion.yaml` stops the asking.

**Installing.** Any refusal keeps the running version and says why in the
node's own words:

1. Fetch from `api.github.com`, `github.com` and
   `objects.githubusercontent.com` only, each address checked as public,
   within a size and a time limit (ADR-0041's client).
2. Verify the signature of `SHA256SUMS`, then the binary's sum.
3. Refuse a version that is not newer than the running one, a release
   whose `env` is above the image's ("rebuild the image first"), and a
   binary for another platform.
4. Store it as `<data>/releases/<version>/ganglion` (`0500`) with its
   sums and signature. The node keeps the active and the previous release.
5. Copy the database (`VACUUM INTO <data>/releases/<version>/before.db`).
6. Record the version as pending and exit with code 75.

**Starting.** `ganglion run` is also the launcher:

- At start the binary reads `<data>/releases/state.json`. When it names a
  release other than the running binary, the node verifies that file again
  (signature and sum, as at the install) and replaces its own process with
  it (`exec`). The binary of the image or the package is never replaced:
  it is the floor.
- A pending release gets two attempts. The launcher counts an attempt
  before the `exec`; the new version clears the count when its components
  have started and stayed up for two minutes, and tells the admins it
  runs.
- After two failed attempts the launcher marks the release failed, goes
  back to the previous version and alerts the admins. When the failed
  version had applied a database migration, the copy from step 5 is put
  back first; otherwise the database is left as it is.
- `/update rollback` is the same way back, at the owner's command.

**What it does not update.** The image: the Rust toolchain, the claude
CLI, the entrypoint. That stays `railway up`, rare, and a release says
when it is needed (`env`).

**What it does not cover: the first install** (owner, 2026-11-13). A
machine with no node has nothing to run `/update` on. A script that takes
it to a running node, with one working channel, is a step of its own
(spec section 14). It installs the releases made here, so one thing is
fixed now: the signature is a plain Ed25519 signature that `openssl` can
check, and the public key is published in the repository, so a shell
script verifies a release before any ganglion code runs.

## Security

- **Trust boundaries.** The release channel reaches the trust root: what
  it serves becomes the node. This is the boundary of ADR-0041 one level
  up, and here no ceiling stands above the code, so the signature is
  required from the first version.
- **What an attacker controls.** The network path; a release on the
  project's GitHub (a stolen account or token); the data volume, for one
  who already runs code as the node's user; the model's output; a person
  who is not an admin.
- **Abuse cases:**
  - *A tampered download, a redirect to another host.* The host list, the
    sum, the signature.
  - *A release made with a stolen GitHub token.* It lacks the signature:
    the key is not in the repository. A stolen signing key is the case
    this does not stop; the key is kept as a workflow secret, used only on
    a core tag from `master`, and replaced by a release signed with the
    old one.
  - *A downgrade to a version with a known hole.* A version not newer
    than the running one is refused; only `/update rollback` goes back,
    and only to the one version the node kept.
  - *A file planted in `<data>/releases`.* The launcher verifies the
    signature at every start, not only at the install, so a file that is
    not a release of the project is never run.
  - *The agent updates the node, or a prompt injection asks it to.* There
    is no tool: the command is the owner's, and it is not level 5 of spec
    9.8, since the agent builds nothing and chooses nothing. A host
    command cannot write the folder: it is the node's own
    ([ADR-0039](0039-command-sandbox.md)).
  - *Text from the release page in a reply or the history.* Only the
    version, the date and the link are shown; the release's notes are
    outside content and go nowhere.
  - *A new version that does not start, or loops.* Two attempts, then the
    previous version and an alert; the database copy covers a migration.
  - *A person who is not an admin, or an admin in a group.* Refused, as
    `/secrets` is.

## Consequences

- The owner updates the node from the chat in under a minute and sees
  which version runs. A node on a Pi updates the same way.
- A release is a deliberate act: a tag. `master` and "what runs" are
  different things by design, and `/update` shows the gap.
- **To build:** the release workflow and the signing key; `update` (the
  client, the verification, the store of two releases, the state file);
  the launcher step in `run`; the health mark; `/update` and
  `ganglion update`; the daily check and its setting; the releases folder
  among the node's own files in the sandbox; the image's `env` number;
  the runbook.
- **Given up:** updates of the image stay manual. A rollback across a
  migration loses what the failed version wrote, which is at most its two
  starts.
- The first signed release still reaches the node by `railway up`: a node
  cannot verify a signature before it carries the key. A new machine has
  the same gap until the install script exists (spec section 14).
- Module releases can take the same signature, which closes the gap
  ADR-0041 left to P3.

## Open before the plan

- **Whether the data volume lets a program run from it** on Railway (a
  `noexec` mount). When it does not, the verified binary is copied to a
  folder that does (the user's cache) before the `exec`. To check on the
  owner's service; the plan's first task.

Settled with the acceptance: the work is task 6.7 of M6, and the daily
check is on by default.

## Revisit when

- A release must change the image often: then the image by tag (B) joins
  this for the environment.
- A second maintainer signs releases, or the key must be kept outside
  GitHub.
- Nodes of the P2 network must be updated together, or by a core node on
  their behalf.
