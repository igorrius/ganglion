-- The link an event or a call run came over, as `a.x → b.y` (R-RUN-20,
-- ADR-0108). Empty for a run another trigger started.

ALTER TABLE module_runs ADD COLUMN link TEXT NOT NULL DEFAULT '';
