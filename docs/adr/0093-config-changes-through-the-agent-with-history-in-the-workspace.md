# ADR-0093: Change the node's file through the agent on an allowlist, with its history in the workspace repository

- **Status:** Accepted
- **Date:** 2026-11-16
- **Deciders:** Igor Artasevych
- **Spec:** [7.14 Node configuration](../spec.md#714-node-configuration)
  (R-CFG-13 – R-CFG-16), [10.2 P1](../spec.md#102-p1--all-in-one--scope-and-acceptance)
  (M6 tasks 6.1–6.3); [design note](../plans/p1-m6-config-design.md)

## Context

M6's acceptance says the agent applies a configuration change the owner
approved, refuses one that fails the config's checks, and rolls back on
request (tasks 6.1–6.3 of the [phase plan](../plans/p1-all-in-one.md)).

What exists on 2026-11-16:

- `ganglion.yaml` is the node's single source of configuration
  ([ADR-0059](0059-node-config-file.md), R-CFG-1 – R-CFG-12). It lives
  beside the database, or at the path `--config` / `GANGLION_CONFIG`
  gives; deployed nodes rely on that path.
- The node already rewrites parts of it: module lines (`SetModule`,
  `RemoveModule`), `destinations`, a module's `notify_to` and
  `telegram.groups`. Each writer edits the YAML node tree, keeps comments
  and order, and replaces the file through a temporary file and a rename.
  Each change has its own tool or card, confirmed by the owner.
- No change to the file is recorded. Nothing shows what changed, when, by
  whom and why, and nothing can undo it.
- The agent's workspace (memory, persona, module sources) is a git
  repository driven by `gitrepo`, with a commit per change and reverts
  (ADR-0024, ADR-0033).
- Modules apply at once; every other section applies at the next start,
  and `/restart` ends the node for its supervisor (R-CFG-6).
- Some fields open a security boundary when changed: the public listener
  and the update mode (R-CFG-11, already on a card of their own), secret
  references, the sandbox, a model provider's `base_url` (where a key is
  sent) and `command` (what the node runs), `telegram.api_url` (where the
  bot token is sent) and `telegram.admin` (who owns the node).

The access list beyond `telegram.admin` is in the database, not in the
file; changing it from the chat is its own task
([#235](https://github.com/igorrius/ganglion/issues/235)).

Three questions need a decision: where the history lives, which fields
the agent may change, and how a rollback works. How a change takes effect
was decided with them (owner, 2026-11-16): it is written and applies at the
next start, with a restart offered on the same card; changing fields with
no restart waits in "Not now".

## Options

Where the history lives (the file itself stays where it is, R-CFG-1):

### A. A copy in the workspace repository

Every write to the file also copies it to `config/ganglion.yaml` in the
workspace and commits the copy with a reason and a source. At start the
node compares the live file with the last copy and commits a difference
as a change made on the host.

### B. Versions in the database

A table holds each version's full text, the reason, the source and the
time. A rollback writes an older text back.

### C. A git repository around the file

The directory that holds the file becomes a repository that tracks only
that file.

## Comparison

| Criterion | A. Workspace copy | B. Database rows | C. Repository around the file |
|---|---|---|---|
| Code that exists | `gitrepo` (commit, revert; a log and a diff to add) | none: diff and history by hand | `gitrepo`, pointed elsewhere |
| History the owner can read on the host | `git log` in the workspace | a query | `git log` beside the database |
| Two copies of the file | yes; checked at each start | no (rows are not a file) | no |
| Risk | the copy drifts after a crash between the write and the commit; caught at the next start | none new | the directory also holds `master.key` and the database: one wrong ignore rule puts the key into history for good |
| Fits the phase plan ("next to memory") | yes | no | no |
| Moves with the workspace (M7) | yes | with the database | no |

## Decision

**A: a copy in the workspace repository** (owner, 2026-11-16). It reuses
`gitrepo`, keeps the history next to memory as the phase plan says, and
does not put a repository beside the master key. The drift between the
copy and the live file is the same case as an edit made on the host, and
both are caught at start.

Decided with it (owner, 2026-11-16):

- **An allowlist.** `config.propose` takes only the fields listed in
  R-CFG-14. Everything else is changed on the host, or by the tool that
  owns it (modules, destinations, groups, the update mode).
- **Written now, applied at the next start.** The card that applies a
  change offers "Restart now", the same path as `/restart`.
- **A rollback is a new proposal.** `config.rollback` takes the allowlisted
  fields a recorded change touched and proposes their earlier values. It
  is not a `git revert`, so it never conflicts with later changes to other
  lines, and it cannot undo a module line or a route behind the back of the
  tools that keep them consistent with the node's state.

## Security

- **Trust boundaries.** The agent's tool call crosses into the node's
  configuration, which decides limits, models and language for every later
  turn. The owner's press is the boundary; the allowlist bounds what a
  press can do.
- **What an attacker controls.** Text that reaches a session (a web page, a
  forwarded message, a group member) can steer the model into proposing a
  change. Someone with write access to the workspace could edit the copy.
- **Abuse cases and what stops them.**
  - *An injected request to widen the node* (the sandbox, the public
    listener, a provider's address or command, a secret reference, the
    admin): the field is not on the allowlist, so `config.propose` refuses
    it in code and no card is shown.
  - *An injected request to raise a limit*: every change needs the owner's
    press, the card shows each field's old and new value, and a proposal
    from a tainted session carries a line that says so (R-POL-6).
  - *A card approved after the file changed under it*: the press checks the
    file again and shows what changed instead of applying blindly.
  - *A forged history*: tools that write the workspace refuse `config/`;
    the live file stays the truth, and the copy is only its record, so an
    edited copy is overwritten and committed at the next start.
  - *A value that breaks the node*: the change is parsed strictly and
    checked against the schema and the modules' manifests before the card,
    and again before the write.

## Consequences

- Every change to the file, by any writer, has a commit with a reason and
  a source; the owner asks "what changed" and gets an answer.
- Routine settings change from the chat with one press and one restart.
- The existing writers go through one recorder, which adds a git commit to
  their path; a node without git keeps working with no history.
- The allowlist is a table kept in step with the schema by a test; a new
  field is not changeable from the chat until it is added on purpose.
- To build: `settings.SetPath`, the allowlist, the recorder and its check
  at start, `config.propose`, `config.history`, `config.rollback`, the
  restart button, chatlab scenarios, a page in the user guide.

## Revisit when

- A field on the allowlist needs to apply without a restart: that field
  gets its own reload path (in "Not now" until then).
- The owner needs to change a field outside the allowlist from the chat
  often enough that a card of its own is worth it, as `telegram.updates`
  has.
- M7 moves a node's workspace to another host: the copy of the file goes
  with it, and a node started from the copy needs a rule.
