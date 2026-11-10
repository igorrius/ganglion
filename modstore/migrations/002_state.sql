-- A module's persistent key-value state (R-RUN-3, runner.NewSQLState): it
-- survives restarts and version swaps. Keyed by the module's name.

CREATE TABLE module_state (
    module TEXT NOT NULL,
    key    TEXT NOT NULL,
    value  TEXT NOT NULL,
    PRIMARY KEY (module, key)
);
