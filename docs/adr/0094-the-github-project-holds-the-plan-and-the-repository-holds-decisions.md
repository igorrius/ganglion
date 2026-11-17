# ADR-0094: Keep the plan and the status in the GitHub Project, and decisions in the repository

- **Status:** Accepted
- **Date:** 2026-11-17
- **Deciders:** Igor Artasevych
- **Spec:** [10. Plan: phases and gates](../spec.md#10-plan-phases-and-gates)

## Context

Since 2026-11-14 work is tracked in GitHub Issues and on the
[ganglion project](https://github.com/users/igorrius/projects/13):
milestones group the work, an issue says what is to be done and is closed
by the pull request that does it, the project's Status shows what is in
progress.

The repository kept the same information a second time: a phase plan
(`docs/plans/p1-all-in-one.md`) with a table of tasks and their state, 28
step-by-step implementation plans with ticked boxes, a "Where we are"
section of about 700 lines in `docs/README.md`, and a chronicle of closed
milestones inside the spec's section 10. Each closed task had to be
written down in three or four places by hand, and the copies drifted: on
2026-11-17 "Where we are" still named M6 as the next milestone after it
had closed, and the phase plan still planned a cutover from the old bot
that the owner had done on 2026-11-13.

The owner does not want to keep two records of the plan in step, by hand
or half by hand (owner, 2026-11-17).

## Options

### A. Keep both

The repository keeps its plans and status beside the issues, and every
close updates both.

### B. The GitHub Project holds the plan; the repository holds decisions

Status, tasks, their order, dates and step-by-step plans live only in
GitHub: a milestone per milestone of a phase, with its goal and "done
when" in the milestone's description; an issue per task, a parent issue
with sub-issues for a task of several pull requests, the steps of a plan
as a task list in the issue. The repository keeps what is decided and
why: the spec (requirements, the phases' scope and gates), the ADRs,
the design notes, the research notes, the architecture views, the
conventions, the runbooks and the user guide.

### C. Generate the repository's status from GitHub

A script writes "Where we are" from the issues and milestones. Nothing is
kept by hand, but the page is a copy nobody reads in the repository when
the project shows the same thing live.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Work at each close | Three or four places | The issue, closed by its PR | The issue, plus a run of the script |
| Copies that drift | Yes, seen on 2026-11-17 | None | None, if the script runs |
| Plan visible with dates | No | The project's roadmap by milestone or by date fields | As B |
| Plan readable offline, in a clone | Yes | No | Yes, as of the last run |
| Something to build | No | No | A script and a schedule |

## Decision

**B.** The plan and the status are kept once, in the GitHub Project and
its issues; the repository keeps decisions and their reasoning.

- `docs/plans/` is gone. Its design notes (`*-design.md`) were reasoning
  that ADRs and the spec cite: they moved to [`docs/design/`](../design/),
  with only their links and their mentions of the removed status page
  changed. The step-by-step plans and the phase plan were records of
  work: they were deleted, and a link to one points to its copy at the
  last commit that held it.
- `docs/README.md` is an index of the engineering record; "Where we are"
  is gone. Operating notes it held moved to the user guide or the
  runbooks where they are still true.
- The spec's section 10 keeps each phase's scope and gate; the chronicle
  of closed milestones is gone.
- A new piece of work starts as an issue. Its plan, when it needs one, is
  a task list in the issue or its parent; a design that needs reasoning
  is a design note in `docs/design/` or an ADR.
- Moving links in accepted ADRs to the new paths is not a rewrite: the
  decision text stays as it was.

## Security

No new boundary: this moves documentation between the repository and the
project, and no part of the node reads either.

## Consequences

- A closed task is recorded once, by closing its issue.
- The project's roadmap and the milestones' due dates show the plan with
  dates.
- A clone of the repository no longer shows the plan or the status:
  reading them needs GitHub.
- The record of how past milestones were built stays in git history and
  in the closed issues, not in the tree.

## Revisit when

The project moves off GitHub, or the plan has to be read where GitHub is
not reachable.
