-- The registries list loses its "default".
--
-- A default that nothing reads is a lie told in the interface: an operator marks one
-- registry as the default and every deployment still pulls from wherever the images were
-- pushed, and the only symptom is a row of settings that appear to work and do not. It
-- also invited the question it could never have an honest answer to — if the default
-- changes, does everything that chose "the default" move? — when in fact nothing was
-- choosing it.
--
-- What replaced it is a value, not a pointer. A place names the registry it pulls from;
-- an unnamed one is refused with a sentence saying so, because a place that quietly
-- deploys from somewhere nobody chose is a deployment nobody can point at afterwards. The
-- deploy module's own setting for the value a new place starts with is a creation-time
-- default and nothing else: changing it moves no place.
--
-- The flag was never read, so nothing is lost by dropping it. The uniqueness it carried —
-- at most one record marked — goes with it, and a table where two rows may both be
-- ordinary is a table with one less thing to get wrong.
ALTER TABLE docker_registries DROP COLUMN IF EXISTS is_default;
DROP INDEX IF EXISTS docker_registries_default_key;
