-- When a press answered the call, so a folded question shows the time of the
-- decision however late it is drawn (R-POL-9). 0 when no press did, or the press
-- came before this column.
ALTER TABLE pending_calls ADD COLUMN answered_ms INTEGER NOT NULL DEFAULT 0;
