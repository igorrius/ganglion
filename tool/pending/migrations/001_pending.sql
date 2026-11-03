-- Calls awaiting the owner's confirmation and the buttons that answer
-- them (ADR-0032). A button carries only its random id to the chat; what
-- it does is stored here. Times are Unix ms UTC.
CREATE TABLE pending_calls (
    id         TEXT    PRIMARY KEY,
    turn_id    TEXT    NOT NULL,              -- the call's turn; with n, its key (R-DLV-6)
    n          INTEGER NOT NULL,
    conv       TEXT    NOT NULL,
    transport  TEXT    NOT NULL DEFAULT '',
    principal  TEXT    NOT NULL,              -- the person asked; only they may answer
    tool       TEXT    NOT NULL,
    args       TEXT    NOT NULL,              -- as the model gave them: the call runs with these
    question   TEXT    NOT NULL,              -- redacted
    status     TEXT    NOT NULL,              -- waiting | approved | denied | expired | done
    result     TEXT    NOT NULL DEFAULT '',
    created_ms INTEGER NOT NULL,
    expires_ms INTEGER NOT NULL,
    UNIQUE (turn_id, n)
);
CREATE INDEX pending_calls_status ON pending_calls (status, expires_ms);

CREATE TABLE pending_buttons (
    id         TEXT    PRIMARY KEY,           -- 26 chars of Crockford base32 from 16 random bytes
    pending    TEXT    NOT NULL DEFAULT '',   -- the pending call; '' for a quick action
    action     TEXT    NOT NULL,              -- approve | deny | a quick action's name
    label      TEXT    NOT NULL DEFAULT '',
    principal  TEXT    NOT NULL,
    conv       TEXT    NOT NULL,
    args       TEXT    NOT NULL DEFAULT '',   -- a quick action's arguments, fixed when the button was made
    expires_ms INTEGER NOT NULL,
    used       INTEGER NOT NULL DEFAULT 0,
    used_by    TEXT    NOT NULL DEFAULT ''    -- the event that pressed it; its rerun gets the same press
);
CREATE INDEX pending_buttons_pending ON pending_buttons (pending);
