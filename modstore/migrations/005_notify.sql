-- Messages a module sent each day (R-RUN-16, ADR-0066).
CREATE TABLE notify_days (
  module  TEXT NOT NULL,
  day     TEXT NOT NULL,          -- YYYY-MM-DD in the node's time zone
  sent    INTEGER NOT NULL DEFAULT 0,
  limited INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (module, day)
);
