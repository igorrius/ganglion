-- A group chat that is not connected and has addressed the bot (R-DLV-17).
CREATE TABLE group_requests (
    grp      TEXT PRIMARY KEY,           -- the group's key, e.g. telegram:-1001234567890
    title    TEXT NOT NULL DEFAULT '',   -- outside text
    status   TEXT NOT NULL,              -- pending | refused
    asked_ms INTEGER NOT NULL
);
