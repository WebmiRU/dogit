-- A job remembers why it failed.
--
-- A runner says, when it finishes a job, what went wrong — and the core threw it away.
-- What was left was a red badge and, when the job died before printing anything, a log
-- with nothing in it: the two things a reader needs most are the verdict and the reason,
-- and only one of them was kept.
--
-- The column is the job's own sentence about itself, written by whatever ran it. Empty
-- for a job that succeeded, and for a job nobody has finished yet.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT '';
