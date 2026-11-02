-- The tool-call journal (design section 4, rule 7). One row per decision
-- on a call: a call that waits for confirmation and is approved later has
-- two rows; a turn that reruns after a crash replaces its own rows. Args
-- are redacted and capped before they get here. Times are Unix ms UTC.
CREATE TABLE tool_calls (
    key         TEXT    NOT NULL,              -- "<turn id>:<call number>"
    decision    TEXT    NOT NULL,              -- allowed | confirm | approved | denied | expired | forbidden | out_of_grant
    turn_id     TEXT    NOT NULL,
    n           INTEGER NOT NULL,
    tool        TEXT    NOT NULL,
    principal   TEXT    NOT NULL DEFAULT '',
    args        TEXT    NOT NULL DEFAULT '',
    result_head TEXT    NOT NULL DEFAULT '',
    result_len  INTEGER NOT NULL DEFAULT 0,    -- runes
    err         TEXT    NOT NULL DEFAULT '',
    ms          INTEGER NOT NULL DEFAULT 0,
    tainted     INTEGER NOT NULL DEFAULT 0,
    at_ms       INTEGER NOT NULL,
    PRIMARY KEY (key, decision)
);
CREATE INDEX tool_calls_at ON tool_calls (at_ms);
