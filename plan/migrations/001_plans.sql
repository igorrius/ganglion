-- One plan per conversation (R-LIVE-8, ADR-0052). Times are Unix milliseconds UTC.
CREATE TABLE plans (
    conv         TEXT    PRIMARY KEY,
    idem_key     TEXT    NOT NULL,              -- the key of the call that first showed it
    steps        TEXT    NOT NULL CHECK (json_valid(steps)),
    version      INTEGER NOT NULL,
    active       INTEGER NOT NULL,
    dirty        INTEGER NOT NULL,              -- it changed since the last edit
    last_edit_ms INTEGER NOT NULL,
    updated_ms   INTEGER NOT NULL
);
