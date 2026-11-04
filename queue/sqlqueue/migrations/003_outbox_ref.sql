-- Where a sent message went, as the transport names it (a Message.Ref), so a
-- later turn can edit it: an expired question closes itself.
ALTER TABLE outbox ADD COLUMN ref TEXT NOT NULL DEFAULT '';
