-- Stored secrets (ADR-0077): each value sealed with AES-256-GCM under the
-- node's master key, with its scope and name as additional data.
CREATE TABLE secrets (
    scope      TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    nonce      BLOB    NOT NULL,
    ciphertext BLOB    NOT NULL,
    set_ms     INTEGER NOT NULL,
    set_by     TEXT    NOT NULL,
    PRIMARY KEY (scope, name)
) STRICT;

-- What the store keeps about itself: the check value that tells whether
-- the key fits the rows.
CREATE TABLE secret_meta (
    key        TEXT PRIMARY KEY,
    nonce      BLOB NOT NULL,
    ciphertext BLOB NOT NULL
) STRICT;
