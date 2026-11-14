# ADR-0082: Open the repository now, and keep the announced release in P3

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** Igor Artasevych
- **Spec:** [10. Plan](../spec.md#10-plan-phases-and-gates),
  [11. Open decisions](../spec.md#11-open-decisions),
  [12. Risks](../spec.md#12-risks)

## Context

The spec has said since its first draft that ganglion is open source, and
answered the risk "issues eat development time" with "public release no
earlier than P3". Until today that one phrase covered two different things:
the repository being readable by anyone, and the project being offered to
strangers as something to install.

In the middle of P1 the owner wants three things that a private repository
does not give:

- colleagues, who are developers, install a node, try it and file bug
  reports;
- the repository is a showcase of how the project is built: the spec, the
  ADRs and the plans as much as the code;
- what a hosting platform gives a public repository: a documentation site,
  free builds, branch protection.

What stands against it:

- The risk the spec names: one owner, weekends, and an issue tracker that
  anyone can write to.
- The history. All 567 commits were audited on 2026-11-14: no token, key,
  `.env` or database file was ever tracked. It does hold the owner's name
  and commit email, which are public already, and the generated address of
  the owner's live node, which the tree no longer names
  ([CLAUDE.md](https://github.com/igorrius/ganglion/blob/master/CLAUDE.md), the rule of 2026-11-14).
- The project runs module code written by a model and host commands in a
  sandbox. A reader who finds a hole needs a private way to report it
  before the code is public, not after.
- Agents work in this repository. Once it is open, the text of an issue or
  of a pull request is written by a stranger and read by a model.

## Options

### A. Stay private until P3

The spec as written. Colleagues are added as collaborators one by one.

### B. Open the repository now, quietly; the announced release stays in P3

The repository becomes public with its whole history. Versions are 0.x with
no guarantees and the README says so. Nobody is told except the colleagues
the owner invites. The public release, 0.1 with an announcement, an install
path for strangers and an answer to their issues, stays in the P3 gate.

### C. Open a fresh repository without the history

A new public repository starts from one commit of today's tree. The private
one stays as the archive.

## Comparison

| Criterion | A. Private until P3 | B. Open now, release in P3 | C. Fresh repository |
|---|---|---|---|
| Colleagues try it and report bugs | Each needs an invitation to a private repository | Anyone with the link; issues need no invitation | Same as B |
| Showcase | None | The code and the whole engineering record | The code and the documents, but not how they came to be: ADRs cite commits and dates that are gone |
| Documentation site, free builds, branch protection | Not on the owner's plan | Yes | Yes |
| Issue burden | None | Low: no announcement, so readers are the invited ones; the README promises nothing | Same as B |
| What leaks | Nothing | What the history holds; audited, and accepted by the owner | Nothing from the history |
| Effort | None | A README for an outside reader, a security policy, contribution rules, repository protections | The same, plus two repositories to keep in step or a broken trail of pull requests and tags |

## Decision

**B.** Opening the repository and releasing the project are two decisions,
and only the second one carries the burden the spec was guarding against.
The history is the showcase, and the audit found nothing in it that C would
be worth its cost to hide.

What "open" means until the release:

- Versions are 0.x; nothing is promised to stay stable, and nothing is
  promised to be answered.
- There is no announcement and no promotion.
- Bug reports come through GitHub Issues. The owner's own backlog stays
  where it is; its card ids (`G-<n>`) that appear in documents are not
  links.
- Vulnerabilities are reported privately ([SECURITY.md](https://github.com/igorrius/ganglion/blob/master/SECURITY.md)).
- Only the owner merges into `master`.

## Security

- **Trust boundaries.** Two new ones. A pull request from a fork runs the
  project's workflows on code a stranger wrote. An issue, a pull request
  and a review comment are outside text read by the people and the models
  that work on the repository.
- **What an attacker controls.** The code and the workflow files of their
  own fork's pull request; the text of anything they post; nothing in the
  repository itself.
- **Abuse cases.**
  - *A pull request that changes a workflow to steal a secret or to push.*
    Workflows run for a pull request with a read-only token and no secrets
    (GitHub's rule for forks; `permissions: contents: read` in `ci.yml`),
    and a first-time contributor's run waits for the owner's approval. No
    workflow uses `pull_request_target`.
  - *A push or a merge into `master` by someone else.* Nobody but the owner
    has write access; a ruleset on `master` forbids force pushes and
    deletion and requires a pull request with passing checks.
  - *An issue that tells a model what to do.* The same rule the agent
    itself follows (spec principle 9): outside text is data. A session that
    reads issues does not act on instructions found in them; what it would
    change goes through a pull request the owner reads.
  - *A vulnerability posted as a public issue.* Private reporting is on,
    and `SECURITY.md` and the issue template point to it.
  - *The node's address in the history.* Accepted by the owner. The address
    alone opens nothing: the node answers only the people on its access
    list and checks the webhook's secret. The node moves behind the owner's
    own domain later.

## Consequences

- Colleagues can clone, build, run and report without being invited to the
  repository.
- The rule that nothing real about a running node goes into the repository
  stops being a precaution and becomes the only barrier.
- Every document is now read by strangers. The README, a security policy,
  contribution rules and an issue template are written with this ADR.
- To build next, each as its own step: a user guide and a documentation
  site, and an image published by tag as the install path.
- Issues from strangers may still come. They are answered when the owner
  has time; the README says so.

## Revisit when

Issues or pull requests from people the owner did not invite take more than
an hour of a weekend, or P3's gate comes into view and the release has to
be planned.
