-- One row per host command call (R-CMD-1, R-CMD-4, ADR-0038): the call key
-- makes a command run once, across a rerun and a restart. Times are Unix
-- milliseconds UTC.
CREATE TABLE host_jobs (
    key        TEXT    PRIMARY KEY,           -- the call's key, <turn id>:<n>
    conv       TEXT    NOT NULL,
    transport  TEXT    NOT NULL,
    principal  TEXT    NOT NULL,
    depth      INTEGER NOT NULL,
    argv       TEXT    NOT NULL CHECK (json_valid(argv)),
    dir        TEXT    NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('running', 'done', 'interrupted')),
    background INTEGER NOT NULL DEFAULT 0,    -- its call returned before it ended
    announced  INTEGER NOT NULL DEFAULT 0,    -- its command.done event is stored
    text       TEXT    NOT NULL DEFAULT '',   -- the outcome as the model reads it, redacted
    started_ms INTEGER NOT NULL,
    ended_ms   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX host_jobs_open ON host_jobs (state, background, announced);
