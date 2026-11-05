-- Which turn took an event that waited behind it (R-LIVE-1, ADR-0050); NULL for an
-- event that had its own turn.
ALTER TABLE inbox ADD COLUMN taken_by TEXT;
