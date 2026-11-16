# ADR-0091: Keep a bounded journal of each module's runs, with the chain that started a run copied into it

- **Status:** Accepted
- **Date:** 2026-11-16
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-15), [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-12, R-RUN-15, R-RUN-16, R-RUN-20), [7.9](../spec.md#79-observability) (R-OBS-8, R-OBS-10)
- **Issue:** [#169](https://github.com/igorrius/ganglion/issues/169); plan: [module-run-journal.md](../plans/module-run-journal.md)

## Context

On 2026-11-11 a battery alert arrived in the owner's chat, and nothing in
the chat showed how it came about: which module ran, what started it and
what it called. A successful run leaves only counters: the runs and
failures of a version (`module_versions`), the messages of a day
(`module_notify`), the cells' history and the metrics. There is no log
line for a run that went well and no record of a single run. `/last`
shows only the agent's turns ([R-OBS-10](../spec.md)).

The owner wants the chain in the chat, with no model call: "demo-reader
ran at 16:30 by cron and wrote its cells, the change of soc started
demo-alerts v1, which read 3 cells and called notify once, and that is
this message".

What a record must respect:

- The model and the chat never get a module's text (R-RUN-13, R-RUN-16):
  no notify text, no cell value, no module error. A cell id or a module
  name is manifest text and is shown only where it fits the node's name
  pattern, as `module.status` does already.
- A module can run every minute, so the record must have a bound.
- A chain has up to 4 links (R-RUN-15). The producer's run is settled
  before its listeners' events are put, so its record exists by the time
  a listener's run ends.
- A run delivered again after a restart has the same id (the cron slot,
  the call's key, the trigger event's id) and must not be counted twice.

## Options

### A. A table of runs, the upstream chain copied into each run at write

A table `module_runs`, one row per run keyed by module and run id: the
version, the cause (cron; a tool call and its name; a change of cells by
another module's run), when, how long, the outcome in the node's closed
sets, the host calls by kind with counts, the cells written, the messages
sent. A run from a cell trigger copies the producer's row (and the chain
that row holds) into its own when it is written. Each module keeps its
last 200 rows. A message records the id of the run that sent it.

### B. A table of runs, the chain followed by links at read

The same table, with only the producer's module and run id in a row. The
chain is followed when it is shown.

### C. Log lines and the diagnostics server only

A log line per run with the same fields, read through `ganglion mcp`.
Nothing new in the database; nothing in the chat.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| The owner sees the chain in the chat, with no model | yes | yes, while the producer's rows last | no |
| A frequent producer (cron every minute) keeps the chain of an older message | yes: the chain is in the message's own row | no: 200 rows of a minute-cron last about 3 hours | — |
| Storage | one row per run, a chain of at most 4 summaries in a listener's row | one row per run | the log's size |
| Work at write | one read of the producer's row per triggered run | none | none |
| Never shows a module's text | yes, by construction | yes | yes |

## Decision

**A.** The message the owner asks about is usually a rare one (an alert)
started by a frequent one (a reader on cron). With links (B) the reader's
row is gone within hours and the chain breaks exactly where it is needed;
copying at most four short summaries into the listener's row keeps the
chain for as long as the message's own run is kept. C does not reach the
chat at all.

The pieces:

- **Rows.** `module_runs` holds the last 200 runs of each module; a write
  prunes the older ones. A run delivered again replaces its row. A
  removed module's rows go with it; the chains copied into other modules'
  rows stay.
- **Host calls.** The runner reports every host call's kind to an
  observer in its dependencies, before the call's own checks, so a denied
  call is counted too. The kinds are a closed set: `http.fetch`,
  `cell.get`, `cell.report`, `notify`, `state.get`, `state.put`,
  `secret.get`, `config.get`. `log`, `now_ms` and `take` are not counted.
  A trial run sets no observer.
- **A message names its run.** `module.notify` and the message's origin
  carry the run's id.
- **Who sees it.** `/why` as a reply to a module's message shows the
  chain, with no model, to an admin. The card of `/modules <name>` gets a
  Runs button and `/modules runs <name>` shows the last 10 runs. The
  agent gets the last runs in `module.status`, and a reply to a module's
  message brings the chain as the node's words.
- **Failures.** A row that cannot be written is logged and counted in a
  metric; it never fails the run, as a failed count never does.

## Security

- **Trust boundaries.** No new boundary: the record is written by the
  node from the node's own facts. A module controls only which host
  functions it calls and how often, and the names in its manifest.
- **A module's text in the chat or the model's context.** Nothing the
  module writes is stored: no arguments, no values, no texts, no error.
  The outcome and the kind are the node's closed sets. Cell ids and module
  names are shown only when they fit the node's name pattern; any other is
  left out, as `module.status` and the reply note do now.
- **Flooding the database.** The rows are bounded per module and pruned
  on write; a chain has at most 4 links, each a fixed set of fields.
- **Reading another conversation's history.** `/why` reads the replied
  message's row by the reply's ref in this conversation's outbox, as the
  reply note does (R-DLV-14), and is for admins only.

## Consequences

- A module's message can be traced back to the cron line or the agent's
  call that started its chain, in the chat, with no model.
- The agent sees what a module did on its last runs and can explain a
  message without reading its text.
- One more table, written on every run: one insert, one prune and, for a
  triggered run, one read of the producer's row.
- A chain is a snapshot: a later run of the producer does not change it.
- A message sent before this change has no run, and `/why` says so.

## Revisit when

- 200 runs a module is too few or too many in practice, or the write per
  run shows on a Pi's storage.
- The network (P2) runs modules on other nodes: a chain then crosses
  nodes, and the producer's row is not in this node's database.
