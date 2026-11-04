-- The change scripts that have started, by call key (R-DLV-6, ADR-0042). A key
-- found here means an earlier run may have done its work, so it is not run
-- again. Times are Unix ms UTC.
CREATE TABLE skill_runs (
    key        TEXT    PRIMARY KEY,     -- "<turn id>:<call number>"
    skill      TEXT    NOT NULL,
    started_ms INTEGER NOT NULL
);
CREATE INDEX skill_runs_started ON skill_runs (started_ms);
