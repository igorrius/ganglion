# ADR-0016: Apply SQLite migrations with a small built-in migrator, per component

- **Status:** Accepted
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** principle 8; [P1 plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-all-in-one.md) decision D-1

## Context

P1 keeps several components in one SQLite file: state cells (M1), the
inbox/outbox and history (M2, M3), cron runs (M4). Each needs schema
changes over time, applied at start, safely, without an external tool.

## Options

### A. Own migrator in package `sqlitedb`

Each package embeds `NNN_name.sql` files and calls
`sqlitedb.Migrate(ctx, db, "<component>", fs)`; applied versions are kept in
`schema_migrations(component, version)`; each file runs in a transaction.

### B. pressly/goose

A mature migration tool with Go and SQL migrations, up and down.

## Comparison

| Criterion | A. Own | B. goose |
|---|---|---|
| Code and dependencies | About 100 lines, none | A library with its own options and dialect layer |
| Per-component versions in one file | Built in | Needs a separate version table per component |
| Down migrations | No (roll forward; backups cover the rest) | Yes |
| Fit with principle 8 | Nothing extra in the binary | More code in a small process |

## Decision

**A.** Per-component versioning is exactly what P1 needs and is trivial to
own; down migrations are not worth a dependency when every node keeps daily
backups (spec 7.8).

## Consequences

- Migrations only move forward; a bad one is fixed by a new file.
- File names are checked; a misnamed file fails start-up instead of being
  skipped.

## Revisit when

A component needs data migrations complex enough to want Go code, or down
migrations become necessary.
