-- Cells owned by modules (R-RUN-11, ADR-0062).
ALTER TABLE cells ADD COLUMN owner TEXT NOT NULL DEFAULT '';
ALTER TABLE cells ADD COLUMN about TEXT NOT NULL DEFAULT '';
CREATE INDEX cell_changes_at ON cell_changes (cell_id, at_ms);
