-- Every delivered part's message, as the transport names it, so a reply to any of
-- them finds its outbox row (R-DLV-14, ADR-0055). An edit with text points its
-- message at the edit's row, which holds the text the message shows now.
CREATE TABLE outbox_refs (
    transport TEXT NOT NULL,
    conv      TEXT NOT NULL,
    ref       TEXT NOT NULL,
    outbox_id TEXT NOT NULL,
    PRIMARY KEY (transport, conv, ref)
);
