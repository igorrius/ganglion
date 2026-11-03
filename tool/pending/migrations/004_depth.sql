-- The depth of the parked call: 0 for a call made in a turn the owner
-- started, one more for each confirmation that resumed the conversation
-- before it (R-AGT-4, ruling R33). The resumed turn's calls carry this
-- depth plus one, so a chain of approvals ends at the policy's MaxDepth.
ALTER TABLE pending_calls ADD COLUMN depth INTEGER NOT NULL DEFAULT 0;
