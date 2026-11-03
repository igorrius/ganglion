-- Who deleted a job: the token of the press or the call that did it, so
-- a rerun of that same press after a crash knows it deleted the job and
-- gives back the same answer (ruling R42). '' for a live job.
ALTER TABLE schedule_jobs ADD COLUMN deleted_by TEXT NOT NULL DEFAULT '';
