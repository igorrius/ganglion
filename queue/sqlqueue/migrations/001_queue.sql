-- Inbox, outbox and conversation state of a node (docs/spec.md, 7.1).
-- Times are Unix milliseconds UTC; 0 means "not set".

CREATE TABLE inbox (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    id             TEXT    NOT NULL UNIQUE,
    idem_key       TEXT    NOT NULL UNIQUE,
    source         TEXT    NOT NULL,
    conv           TEXT    NOT NULL,
    kind           TEXT    NOT NULL,
    payload        TEXT,
    ttl_ms         INTEGER NOT NULL DEFAULT 0,
    at_ms          INTEGER NOT NULL,
    status         TEXT    NOT NULL DEFAULT 'pending', -- pending | done | expired
    lease_token    TEXT    NOT NULL DEFAULT '',
    lease_until_ms INTEGER NOT NULL DEFAULT 0,
    attempts       INTEGER NOT NULL DEFAULT 0,
    done_ms        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX inbox_pending ON inbox (status, conv, seq);

CREATE TABLE outbox (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT    NOT NULL UNIQUE,
    idem_key      TEXT    NOT NULL UNIQUE,
    transport     TEXT    NOT NULL,
    conv          TEXT    NOT NULL,
    body          TEXT    NOT NULL,                     -- JSON of event.Outgoing
    status        TEXT    NOT NULL DEFAULT 'pending',   -- pending | sent | failed
    attempts      INTEGER NOT NULL DEFAULT 0,
    parts_sent    INTEGER NOT NULL DEFAULT 0,
    not_before_ms INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT    NOT NULL DEFAULT '',
    created_ms    INTEGER NOT NULL,
    done_ms       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX outbox_pending ON outbox (status, transport, conv, seq);

CREATE TABLE conv_state (
    conv       TEXT PRIMARY KEY,
    data       TEXT NOT NULL CHECK (json_valid(data)),
    updated_ms INTEGER NOT NULL
);
