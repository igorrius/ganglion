# ADR-0001: Record architecture decisions as ADRs

- **Status:** Accepted
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** [section 13](../spec.md#13-stop-criteria-and-decisions)

## Context

ganglion is a side project developed in bursts, between other projects.
Key choices (runtime, bus, providers, protocol) were made in a long design
conversation; their reasons live only there. After a few weeks away it is
easy to forget why an option was rejected and to reopen a settled question,
or to drift into a choice that contradicts an earlier one.

The spec says *what* we build. It does not keep the *why* and the
alternatives, and a one-line decision log in it is too thin to judge a
decision later.

## Options

### A. Keep the decision log table in the spec

One row per decision: date, decision, one-line reason.

### B. ADRs in the repository

One Markdown file per significant decision: context, options, comparison,
decision, consequences, when to revisit. Numbered, immutable once accepted;
a changed mind is a new ADR that supersedes the old one.

### C. Decisions in an external tool (Claude Docs, Notion)

## Comparison

| Criterion | A. Log in spec | B. ADRs in repo | C. External tool |
|---|---|---|---|
| Keeps the reasoning and rejected options | No | Yes | Yes |
| Versioned with the code, reviewable in PRs | Yes | Yes | No |
| Readable by coding agents in this repo | Yes | Yes | Only with a connector |
| Survives the repository going public | Yes | Yes | Needs a separate export |
| Effort per decision | Minimal | 15–30 minutes | 15–30 minutes |

## Decision

**B.** ADRs live in `docs/adr/`, use [the template](template.md) and are
listed in [the index](README.md). The cost per decision is small compared
with re-deriving a decision months later.

An ADR is required when a decision is hard to reverse or crosses package
boundaries: a dependency, a protocol or storage format, a runtime, a
security boundary, a change to the phase plan. Small local choices stay in
commit messages.

## Consequences

- The spec's decision log becomes a pointer to the ADR index.
- Open questions in spec section 11 become ADRs with status Proposed when
  work on them starts, and Accepted when decided.
- Accepted ADRs are not edited except for status and links; reversing one
  means a new ADR.

## Revisit when

ADRs pile up unread, or writing them starts to block small decisions.
