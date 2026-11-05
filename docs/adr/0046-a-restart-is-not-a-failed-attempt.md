# ADR-0046: Count a run a restart cut short apart from a failed attempt

- **Status:** Accepted
- **Date:** 2026-11-04
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-3, R-DLV-6)

## Context

R-DLV-3 promises that an event is processed again if the process dies, and
3.8 that a `kill -9` during a turn loses nothing. The turn loop gives up on
an event leased more than 3 times (`core.DefaultMaxAttempts`), and the count
goes up on every lease. `ReleaseLeases` (ADR-0019) cleared the lease at start
but kept the count, so a restart counted as a failure.

The agent's crash harness found the result. In a run of 200 kills a message
was dropped: the owner got "Sorry, something went wrong with that message",
its fact was never stored, and no turn had failed. A unit test reproduces it:
three releases of the same event use up its attempts. The same happens outside
the harness when a node restarts three times during one slow turn, as a
flapping host or a few quick deploys can do.

The count also does a job worth keeping. A message that kills the process
(out of memory on a hostile page, a panic in a parser) looks the same as a
kill from outside. Without a bound it would crash the node forever, and each
run pays for its model calls again.

## Options

### A. Refund the attempt at release, with no bound on restarts

`ReleaseLeases` subtracts the attempt. One statement and no schema change.
An event that kills the process is retried forever.

### B. A separate counter with its own, larger bound

A new column `interrupted`. `ReleaseLeases` moves the run from `attempts` to
`interrupted`. The loop gives up on more than 3 failures or more than 10
interruptions.

### C. One counter with a larger limit

Raise `DefaultMaxAttempts` to about 10. No schema change. A handler that
really fails waits a 10-minute lease between tries, so the owner would hear
of it after more than an hour and a half instead of half an hour.

### D. Tell a crash from a restart

Write a clean-shutdown mark on stop and count only runs with no mark. A
SIGKILL from outside (the harness, an out-of-memory killer, a hard deploy)
still looks like a crash, so it only narrows the problem and adds state.

## Comparison

| Criterion | A. Refund | B. Two counters | C. Larger limit | D. Shutdown mark |
|---|---|---|---|---|
| Healthy message survives repeated restarts | Yes | Up to 10 | Up to 10 | Only clean ones |
| A message that kills the process stops | Never | After 11 runs | After 11 runs | After 4 runs |
| Owner hears of a real failure | 30 min | 30 min | Over 90 min | 30 min |
| Schema change | No | One column | No | A mark |

## Decision

**B.** It is the only option that keeps a healthy message through restarts,
keeps the quick apology for a real failure and still stops a crash loop.

The bound is `core.DefaultMaxInterrupted = 10`: above any plausible run of
restarts during one turn, and low enough that a crash loop costs the price of
a few turns.

## Security

- **Trust boundaries:** none new. The change is in how the core counts its
  own runs.
- **What an attacker controls:** the text of a message or a page that reaches
  a turn, and through it possibly a crash of the process.
- **Abuse cases:**
  - A message built to kill the node: stopped after 11 runs by
    `maxInterrupted`, in code. It holds its conversation and costs up to 11
    turns' model calls, and each crash also interrupts the other
    conversations' turns. Before this ADR it stopped after 4 runs: this is
    the price of the change. The give-up alert reaches the admins, and
    `ganglion_turns_interrupted_total` shows the loop.
  - Side effects in the reruns: unchanged. R-DLV-6 keys them by the call, and
    the crash harness checks one commit per fact. More reruns are possible
    now, not a new kind of run.

## Consequences

- A healthy message survives up to 10 restarts in a row.
- A restart during the wait for a retry also refunds that failed attempt.
  The event is still bounded by `interrupted`.
- New metric `ganglion_turns_interrupted_total`: turns started over after a
  restart.
- A turn is still rerun from its start: its model calls are paid for again,
  and the person is not told. Both are in the spec's "Not now" list.

## Revisit when

A turn's progress is kept between steps, so that a rerun no longer pays for
the whole turn, or when several processes share an inbox (ADR-0019's own
signal).
