CREATE TABLE cells (
    id             TEXT    PRIMARY KEY,
    kind           TEXT    NOT NULL,
    backend        TEXT    NOT NULL DEFAULT '',
    desired        TEXT,
    reported       TEXT,
    reported_cause TEXT    NOT NULL DEFAULT '',
    version        INTEGER NOT NULL,
    updated_ms     INTEGER NOT NULL,
    expires_ms     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE cell_changes (
    seq     INTEGER PRIMARY KEY AUTOINCREMENT,
    cell_id TEXT    NOT NULL,
    version INTEGER NOT NULL,
    field   TEXT    NOT NULL,
    value   TEXT,
    cause   TEXT    NOT NULL DEFAULT '',
    source  TEXT    NOT NULL DEFAULT '',
    at_ms   INTEGER NOT NULL
);

CREATE INDEX cell_changes_by_cell ON cell_changes (cell_id, seq);
