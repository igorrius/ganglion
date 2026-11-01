# ADR-0010: Write all documentation in English

- **Status:** Accepted
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** —

## Context

The design discussion and the first spec draft were in Russian; the README
was in English. The repository is meant to go public at P3 (release 0.1),
and coding agents and potential contributors read the docs.

## Options

### A. Russian docs, translate before going public

### B. English for everything in the repository

### C. Both languages side by side

## Comparison

| Criterion | A. Russian, translate later | B. English | C. Both |
|---|---|---|---|
| Ready for a public repo | No: a big translation job at P3 | Yes | Yes |
| Effort while writing | Lowest for the author | Slightly higher | Double, and copies drift |
| Consistent with code identifiers and comments | No | Yes | Partly |

## Decision

**B.** README, everything under `docs/`, code comments and Markdown files
are in English, even when the working conversation is in another language.
Commit messages are outside this rule.

## Consequences

- The rule is in `CLAUDE.md`, so coding agents follow it.
- Private design material (the original design document) may stay in any
  language; the repository holds the English distillation.

## Revisit when

Never, unless the project's audience changes.
