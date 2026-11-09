-- A plan whose question was pressed (R-CMD-7, ADR-0040): its message is that
-- question, and it approves exactly these commands until it closes or an hour
-- after the press.
ALTER TABLE plans ADD COLUMN message_key TEXT NOT NULL DEFAULT '';   -- '' is plan:<idem>:show
ALTER TABLE plans ADD COLUMN approved TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(approved));
ALTER TABLE plans ADD COLUMN approved_ms INTEGER NOT NULL DEFAULT 0;
