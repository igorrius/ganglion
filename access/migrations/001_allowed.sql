-- Who may talk to the bot (docs/spec.md, 7.8). Strangers are not listed.
CREATE TABLE allowed (
    principal TEXT PRIMARY KEY,         -- e.g. telegram:user:123
    role      TEXT NOT NULL,            -- admin | family
    name      TEXT NOT NULL DEFAULT '',
    added_ms  INTEGER NOT NULL
);
