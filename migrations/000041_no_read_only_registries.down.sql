-- The column comes back, empty, and nobody is told which rows had it set.
--
-- That is the best this can do and it is worth saying why rather than writing something
-- confident. A row's mark was a decision about whether a build could push there, and the
-- deployments that followed it are the record of whether that decision held: a registry
-- with images in it was pushed to, whatever it said before. Reconstructing the old column
-- from a name this script cannot remember would be a guess about somebody's setup, and the
-- one reader of the flag refused a push when it was set — so a restored guess would stop
-- deployments that have been working.
ALTER TABLE docker_registries ADD COLUMN read_only boolean NOT NULL DEFAULT false;