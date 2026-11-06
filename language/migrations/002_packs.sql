-- Machine translations of fixed texts (docs/spec.md, R-LANG-5). source_hash is the hash of the
-- English text a row translates, so a changed text is seen as stale. No tool writes this table.
CREATE TABLE packs (
    lang        TEXT    NOT NULL,
    key         TEXT    NOT NULL,
    text        TEXT    NOT NULL,
    source_hash TEXT    NOT NULL,
    model       TEXT    NOT NULL,
    made_ms     INTEGER NOT NULL,
    PRIMARY KEY (lang, key)
);

-- How often a language was attempted on a UTC day, for the daily cap.
CREATE TABLE pack_attempts (
    lang TEXT    NOT NULL,
    day  TEXT    NOT NULL,
    n    INTEGER NOT NULL,
    PRIMARY KEY (lang, day)
);
