-- One row per background job (R-LIVE-9, ADR-0069): work the node runs
-- outside a turn, a module build or a host command past its wait. The row
-- lets a restart end a job it left open honestly, as interrupted, and edit
-- its card once more. Times are Unix milliseconds UTC.
CREATE TABLE background_jobs (
    key        TEXT    PRIMARY KEY,           -- the host job's key or build:<module>:<id>
    kind       TEXT    NOT NULL CHECK (kind IN ('build', 'command')),
    conv       TEXT    NOT NULL,
    transport  TEXT    NOT NULL,
    principal  TEXT    NOT NULL,
    title      TEXT    NOT NULL,              -- a module name or an approved argv
    state      TEXT    NOT NULL CHECK (state IN ('queued', 'running', 'done', 'failed', 'interrupted')),
    count      INTEGER NOT NULL DEFAULT 0,    -- progress: crates built for a build
    started_ms INTEGER NOT NULL,
    ended_ms   INTEGER NOT NULL DEFAULT 0,
    version    INTEGER NOT NULL DEFAULT 0,    -- the card's last edit; 0 is its first message
    shown      TEXT    NOT NULL DEFAULT ''    -- the card's text as last sent
);
CREATE INDEX background_jobs_open ON background_jobs (state);
