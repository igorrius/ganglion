-- The per-turn trace (docs/spec.md, R-AGT-5). One row per handled event;
-- a turn that reruns after a crash replaces its row. No message text in
-- M3a. Times are Unix ms UTC.
CREATE TABLE turns (
    event_id           TEXT PRIMARY KEY,
    conv               TEXT    NOT NULL,
    principal          TEXT    NOT NULL DEFAULT '',
    started_ms         INTEGER NOT NULL,
    finished_ms        INTEGER NOT NULL,
    steps              INTEGER NOT NULL DEFAULT 0,
    route              TEXT    NOT NULL DEFAULT '[]', -- JSON [{provider, model, error}]
    tools              TEXT    NOT NULL DEFAULT '[]', -- JSON [{name, ok, ms}]
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL    NOT NULL DEFAULT 0,
    outcome            TEXT    NOT NULL,              -- ok | step_limit | daily_limit | no_model | command | error
    error              TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX turns_started ON turns (started_ms);
