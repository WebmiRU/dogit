ALTER TABLE jobs
    ADD COLUMN break_on_error BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN check_exit_status JSONB NOT NULL DEFAULT '[]'::jsonb;
