-- The webhook this node set at Telegram: one row while one is set
-- (R-DLV-18). A node set back to polling removes only this one.
CREATE TABLE telegram_webhook (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    url    TEXT NOT NULL,
    set_ms INTEGER NOT NULL
) STRICT;
