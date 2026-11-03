-- Whether the session was tainted when the call was parked: an approved
-- call runs with that taint even if the session ended before the press,
-- so a memory write keeps where it came from (R-MEM-5, R-POL-6).
ALTER TABLE pending_calls ADD COLUMN tainted INTEGER NOT NULL DEFAULT 0;
