-- A rebuild of a module edits the card of the module's earlier build in the
-- conversation instead of sending its own (ADR-0112). card_of is the key of
-- the job whose message the card is, '' when it is the job's own; build is
-- the number of the build the card shows, 0 when it shows none.
ALTER TABLE background_jobs ADD COLUMN card_of TEXT NOT NULL DEFAULT '';
ALTER TABLE background_jobs ADD COLUMN build INTEGER NOT NULL DEFAULT 0;
