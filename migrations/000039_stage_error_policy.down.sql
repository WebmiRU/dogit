ALTER TABLE jobs
    DROP COLUMN IF EXISTS check_exit_status,
    DROP COLUMN IF EXISTS break_on_error;
