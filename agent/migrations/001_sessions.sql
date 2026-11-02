-- Ended sessions of each conversation (docs/spec.md, R-AGT-6). A session
-- archived again (a rerun, or a resumed session that ends) replaces its
-- row. Times are Unix ms UTC; data is the State JSON.
CREATE TABLE sessions (
    conv       TEXT    NOT NULL,
    session    TEXT    NOT NULL,
    started_ms INTEGER NOT NULL,
    ended_ms   INTEGER NOT NULL,
    ended_by   TEXT    NOT NULL, -- idle | new | resume
    turns      INTEGER NOT NULL,
    title      TEXT    NOT NULL,
    data       TEXT    NOT NULL CHECK (json_valid(data)),
    PRIMARY KEY (conv, session)
);
CREATE INDEX sessions_by_end ON sessions (conv, ended_ms);
