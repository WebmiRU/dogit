-- Resources are gone: a module's data is its own business.
--
-- The table existed to let the core provision a database, hand it to a module, and know where
-- that module keeps things. The middle of that is the part that cannot be justified. A module
-- can have no database, can have two, or can keep everything in a file; it can be a program
-- written by somebody else entirely, on a host the core does not run and cannot reach. Every
-- question this table was built to answer — what does it need, which kind, can this instance
-- destroy it — has to be answered by somebody who wrote the module, and answering it by
-- guessing produces a record rather than a database.
--
-- The credentials the core never had any business keeping are the second reason. It could
-- seal a password but not rotate it, not back it up, and not drop the database behind it. A
-- thing nobody owns is a thing nobody can clean up.
--
-- So a module is configured with settings, and the settings it marks secret are its
-- credentials. There is one mechanism for that and it already exists.
--
-- The migrations that built this table, 28 to 31, are deliberately left in place. They have
-- run on some instances and golang-migrate refuses a gap in the version sequence, so removing
-- the files would strand every database that already applied them. They are history now, and
-- this one takes the history down.
DROP TABLE IF EXISTS resources;

-- The two columns on the module row were the shadow of that table: what the core recorded a
-- module's database was called, so that a name would survive the resource being deleted. With
-- no resources there is nothing to outlive and nothing to remember.
ALTER TABLE integrations DROP COLUMN IF EXISTS database_name;
ALTER TABLE integrations DROP COLUMN IF EXISTS database_role;