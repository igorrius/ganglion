-- Scheduled jobs and their runs (spec 7.10, R-SCH-1). A job's text and
-- grant are fixed when it is created; nothing a run reads changes them.
-- Times are Unix ms UTC.
CREATE TABLE schedule_jobs (
    id         TEXT    PRIMARY KEY,
    owner      TEXT    NOT NULL,              -- the principal the job runs as
    conv       TEXT    NOT NULL,              -- where it was made; a reminder is sent here
    transport  TEXT    NOT NULL DEFAULT '',   -- the transport of the replies
    kind       TEXT    NOT NULL,              -- reminder | task
    cron       TEXT    NOT NULL DEFAULT '',   -- five fields in the node's zone; '' for one time
    at_ms      INTEGER NOT NULL DEFAULT 0,    -- the one time; 0 for cron
    text       TEXT    NOT NULL,              -- the reminder's text or the task's instruction
    grant_json TEXT    NOT NULL DEFAULT '',   -- a task's tool.Grant (R-POL-8)
    system     INTEGER NOT NULL DEFAULT 0,    -- made by the node; cannot be deleted
    created_ms INTEGER NOT NULL,
    deleted    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX schedule_jobs_owner ON schedule_jobs (owner, deleted);

-- One row per scheduled run. A row with put_ms 0 is claimed but its event
-- is not in the inbox yet; the next tick puts it again (the inbox keeps
-- one event per key). A run asked with Run now has no row.
CREATE TABLE cron_runs (
    job          TEXT    NOT NULL,
    scheduled_ms INTEGER NOT NULL,
    put_ms       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (job, scheduled_ms)
);
CREATE INDEX cron_runs_unput ON cron_runs (put_ms);
