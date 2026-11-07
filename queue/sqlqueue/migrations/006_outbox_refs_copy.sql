-- A reply reaches a message up to 90 days old while the outbox keeps the message
-- for 7 (R-DLV-13, ADR-0055), so a ref keeps a copy of what a reply needs: the
-- text it showed, cut at 2,000 characters, and its origin (JSON of event.Origin,
-- NULL when it had none), with the time it was sent, by which Prune drops it.
ALTER TABLE outbox_refs ADD COLUMN text    TEXT    NOT NULL DEFAULT '';
ALTER TABLE outbox_refs ADD COLUMN origin  TEXT;
ALTER TABLE outbox_refs ADD COLUMN sent_ms INTEGER NOT NULL DEFAULT 0;
CREATE INDEX outbox_refs_sent ON outbox_refs (sent_ms);

-- Refs recorded before this migration: copied from their row while it is there,
-- dropped when it is gone.
DELETE FROM outbox_refs WHERE outbox_id NOT IN (SELECT id FROM outbox);
UPDATE outbox_refs SET
    text    = (SELECT coalesce(substr(json_extract(o.body, '$.Text'), 1, 2000), '') FROM outbox o WHERE o.id = outbox_refs.outbox_id),
    origin  = (SELECT json_extract(o.body, '$.Origin') FROM outbox o WHERE o.id = outbox_refs.outbox_id),
    sent_ms = (SELECT CASE WHEN o.done_ms > 0 THEN o.done_ms ELSE o.created_ms END FROM outbox o WHERE o.id = outbox_refs.outbox_id);
