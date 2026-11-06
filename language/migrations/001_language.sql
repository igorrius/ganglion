-- Each person's language (docs/spec.md, R-LANG-1). chosen is what they asked for in the chat, seen
-- the last valid language their channel reported; '' means none. notified lists, comma-separated,
-- the languages whose machine-translation notice they have had (R-LANG-6). Times are Unix ms UTC.
CREATE TABLE people (
    principal  TEXT    PRIMARY KEY,
    chosen     TEXT    NOT NULL DEFAULT '',
    seen       TEXT    NOT NULL DEFAULT '',
    notified   TEXT    NOT NULL DEFAULT '',
    updated_ms INTEGER NOT NULL
);
