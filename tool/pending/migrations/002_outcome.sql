-- The whole outcome of a done call, so a rerun of the turn that resumed
-- the conversation gives back what the first run produced (ADR-0032).
ALTER TABLE pending_calls ADD COLUMN external INTEGER NOT NULL DEFAULT 0; -- the result is outside content
ALTER TABLE pending_calls ADD COLUMN out TEXT NOT NULL DEFAULT '';        -- JSON of the call's outgoing messages; '' for none
