-- The journal of a module's runs (R-RUN-20, ADR-0092): when a run started,
-- what caused it, how long it took, how it ended, which host calls it made,
-- which cells it reported and how many messages it sent. The chain of runs
-- that led to a cell-triggered run is copied in when the run is written, so
-- it survives the pruning of the producer's rows. No module text is stored.
-- Times are Unix milliseconds UTC; JSON columns hold lists and maps.

CREATE TABLE module_runs (
  module       TEXT NOT NULL,
  run_id       TEXT NOT NULL,
  version      TEXT NOT NULL DEFAULT '',
  sha256       TEXT NOT NULL DEFAULT '',
  trigger      TEXT NOT NULL DEFAULT '',   -- cron | tool | cell
  tool         TEXT NOT NULL DEFAULT '',
  cells        TEXT NOT NULL DEFAULT '[]', -- JSON: cells whose change started it
  producer     TEXT NOT NULL DEFAULT '',
  producer_run TEXT NOT NULL DEFAULT '',
  depth        INTEGER NOT NULL DEFAULT 0,
  at_ms        INTEGER NOT NULL DEFAULT 0,
  took_ms      INTEGER NOT NULL DEFAULT 0,
  result       TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL DEFAULT '',
  calls        TEXT NOT NULL DEFAULT '{}', -- JSON: host call kind -> count
  wrote        TEXT NOT NULL DEFAULT '[]', -- JSON: cell ids reported
  sent         INTEGER NOT NULL DEFAULT 0,
  chain        TEXT NOT NULL DEFAULT '[]', -- JSON: hops, oldest first
  PRIMARY KEY (module, run_id)
);

CREATE INDEX module_runs_by_time ON module_runs (module, at_ms);
