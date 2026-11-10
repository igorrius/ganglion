-- The journal of a module's versions (R-RUN-9, R-RUN-10, ADR-0061): who asked
-- for a version, in whose words, from which workspace commit, and how it has
-- run. Retired rows of local modules are kept (their files are deleted).
-- Times are Unix milliseconds UTC; 0 means never.

ALTER TABLE module_versions ADD COLUMN "by" TEXT NOT NULL DEFAULT '';
ALTER TABLE module_versions ADD COLUMN note TEXT NOT NULL DEFAULT '';
ALTER TABLE module_versions ADD COLUMN commit_id TEXT NOT NULL DEFAULT '';
ALTER TABLE module_versions ADD COLUMN language TEXT NOT NULL DEFAULT '';
ALTER TABLE module_versions ADD COLUMN runs INTEGER NOT NULL DEFAULT 0;
ALTER TABLE module_versions ADD COLUMN failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE module_versions ADD COLUMN exceptions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE module_versions ADD COLUMN last_failure_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE module_versions ADD COLUMN last_failure_ms INTEGER NOT NULL DEFAULT 0;
