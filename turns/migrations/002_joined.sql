-- How many messages joined the turn while it ran (R-LIVE-1), for /last (R-OBS-10);
-- and the index that finds a conversation's newest turn.
ALTER TABLE turns ADD COLUMN joined INTEGER NOT NULL DEFAULT 0;
CREATE INDEX turns_conv_started ON turns (conv, started_ms);
