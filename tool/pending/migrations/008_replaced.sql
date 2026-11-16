-- The call that replaced this one (ADR-0088): a newer question of the same
-- tool, from a later turn, withdrew it while it waited. '' when none did.
ALTER TABLE pending_calls ADD COLUMN replaced_by TEXT NOT NULL DEFAULT '';
