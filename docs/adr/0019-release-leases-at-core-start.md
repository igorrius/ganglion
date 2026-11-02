# ADR-0019: Release inbox leases when a core starts; one core process owns a database file

- **Status:** Proposed
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-3, R-DLV-4)

## Context

R-DLV-3: a turn takes its event under a lease, 10 minutes by default, so a
slow model turn is not taken twice. Events of one conversation are leased
in order and one at a time (M2 task 2), so a leased event blocks the
events behind it.

If the process dies between `Lease` and `Complete`, the lease stays in
the database. After a restart the conversation is silent until the lease
runs out: up to 10 minutes. The M2 crash harness hit this. It kills the
echo bot with `kill -9` 40 times while 200 messages arrive, then waits 60 s
for every reply. About one run in eight failed: one chat stopped at 39 of
100 replies while every update was already confirmed and in the inbox.
For the family bot this means: the process restarts after a crash, and the
family chat does not answer for up to 10 minutes.

Facts:

- In P1 a node is one process, and its core is the only writer of its
  SQLite file (ADR-0006: SQLite inside a node, NATS between nodes).
- Telegram's `409` stops a second poller (R-DLV-8), but nothing stops a
  second process from opening the same database file.

## Options

### A. Release leases at core start

When the core starts, before its loop runs, it clears the lease of every
pending inbox event (`lease_until_ms = 0`, `lease_token = ''`). Whatever
held them is dead, because one core process owns the file. A cleared token
also makes a late `Complete` from a ghost return `ErrLeaseLost`.

### B. Short lease, extended while the turn runs

Lease for about a minute and extend it from the loop every few seconds
while the handler runs. A dead process stops extending, so its events come
back within a minute. Needs an `Extend` method, a ticker per turn and its
tests.

### C. Keep the 10-minute lease

No change. Give the harness a short lease through an environment
variable.

## Comparison

| Criterion | A. Release at start | B. Short lease + extension | C. Keep 10 min |
|---|---|---|---|
| Silence after a crash and restart | None | Up to ~1 min | Up to 10 min |
| Silence if the process hangs without dying | 10 min (lease) | ~1 min | 10 min |
| Several processes on one database | Breaks leases: needs one owner | Works | Works |
| Code | One `UPDATE` at start, one test | `Extend`, ticker, cancellation, tests | A test-only knob |
| Harness passes reliably | Yes | Yes, with a 60 s deadline barely above the lease | Only with the knob |

## Decision

**A.** In P1 the core is one process per database, and the crash that
matters is the one a supervisor restarts. A gives zero silence after that
restart at the cost of one statement. The rule it relies on, one core
process per database file, is stated in R-DLV-3.

## Consequences

- `sqlqueue.Queue.ReleaseLeases` clears the leases of pending events;
  `node` calls it once at start, before the loop, and logs how many it
  released.
- A second core process on the same file would steal the first one's
  leases and could run a turn twice. Nothing prevents that yet; a lock is
  on the spec's "Not now" list.
- A hung but living process still holds its lease for the full 10
  minutes; the supervisor's restart is what frees it.

## Revisit when

Two processes write one inbox: the edge buffering of P2, several core
replicas, or a hang that a restart does not catch. Then B (or JetStream's
own redelivery, R-DLV-7) replaces A.
