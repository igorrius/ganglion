-- When each conversation's memory review is due (R-MEM-8, ADR-0072): one
-- row per conversation and session, '' for the live session, else an
-- archived one. A turn moves the live row's time; the review timer puts a
-- memory.review event for a due row and removes it unless it moved again.
-- Times are Unix ms UTC.
CREATE TABLE review_due (
    conv      TEXT    NOT NULL,
    session   TEXT    NOT NULL DEFAULT '',
    transport TEXT    NOT NULL,
    principal TEXT    NOT NULL,
    due_ms    INTEGER NOT NULL,
    PRIMARY KEY (conv, session)
);
CREATE INDEX review_due_by_time ON review_due (due_ms);
