-- The host command a done call left running in the background (R-CMD-4): a
-- rerun of the press reads it with the outcome, so the resumed turn lists the
-- command and its end resumes the conversation. '' when there is none.
ALTER TABLE pending_calls ADD COLUMN job TEXT NOT NULL DEFAULT '';
