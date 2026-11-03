-- Whether a done call failed: the final line of its question ("Approved,
-- but it failed.") reads this flag, not the result's text, which a
-- tool may write as it likes (ruling R40).
ALTER TABLE pending_calls ADD COLUMN failed INTEGER NOT NULL DEFAULT 0;
