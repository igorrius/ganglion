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
  second process from opening the same database file. The `409` loser
  keeps running on purpose (M2 task 7), so two processes on one file would
  last until someone stops one.
- Two processes on one file break more than leases: both senders deliver
  the same outbox rows, so a message retried at the same `not_before` is
  sent twice with no crash at all, and a `429` pause held in one
  process's memory does not hold the other.

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
| Several processes on one database | Breaks leases: needs one owner, enforced by a lock | Leases work; the two senders still duplicate | Same as B |
| Code | One `UPDATE` at start and a lock file | `Extend`, ticker, cancellation, tests | A test-only knob |
| Harness passes reliably | Yes | Yes, with a 60 s deadline barely above the lease | Only with the knob |

## Decision

**A.** In P1 the core is one process per database, and the crash that
matters is the one a supervisor restarts. A gives zero silence after that
restart at the cost of one statement. The rule it relies on, one core
process per database file, is stated in R-DLV-3 and enforced: the core
takes an exclusive `flock` on `<db>.lock` before it opens the database and
refuses to start if another process holds it. The kernel drops the lock
when the process dies, even on `kill -9`.

## Consequences

- `sqlqueue.Queue.ReleaseLeases` clears the leases of pending events;
  `node` calls it once at start, before the loop, and logs how many it
  released.
- A second core process on the same file fails at start with a message
  naming the database. On systems without `flock` (not a target today)
  the lock is a no-op and the rule is the operator's.
- Running two instances side by side, as in the M2 live check of `409`,
  needs a second database path (`make live-echo LIVE_DB=…`).
- A hung but living process still holds its lease for the full 10
  minutes; the supervisor's restart is what frees it.

## Revisit when

Two processes write one inbox: the edge buffering of P2, several core
replicas, or a hang that a restart does not catch. Then B (or JetStream's
own redelivery, R-DLV-7) replaces A.
