-- Versions of the modules a node runs (R-RUN-8, ADR-0041). Times are Unix
-- milliseconds UTC.

CREATE TABLE module_versions (
    module       TEXT    NOT NULL,
    sha256       TEXT    NOT NULL,              -- of the .wasm, lower-case hex
    version      TEXT    NOT NULL,              -- "builtin" or the release's semver
    source       TEXT    NOT NULL,              -- "builtin" or the release tag, e.g. html2md/v0.3.0
    state        TEXT    NOT NULL,              -- active | previous | failed | retired
    reason       TEXT    NOT NULL DEFAULT '',   -- why it failed or was retired
    installed_ms INTEGER NOT NULL,
    PRIMARY KEY (module, sha256)
);
