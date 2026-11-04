-- How many runs of an event a process death or a restart cut short; such a
-- run no longer counts in attempts (ADR-0046).
ALTER TABLE inbox ADD COLUMN interrupted INTEGER NOT NULL DEFAULT 0;
