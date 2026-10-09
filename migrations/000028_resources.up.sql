-- A resource is something this instance gave to a module, or could.
--
-- A database, an object store, a queue: a thing with a name, a kind, an address and a
-- password, that a module needs in order to do its work and that nobody wants to be asked
-- for twice. Until now the only one of these was a database, and it lived as two columns on
-- the module: `integrations.database_name` and `database_role`. That was enough for one kind
-- and one of them, and it had three things wrong with it that this table fixes rather than
-- papers over.
--
-- It had no kind, so there was nowhere to say *what* had been given and nothing that could
-- compare one against what a module says it needs. A module that asks for `db:postgresql`
-- and one that asks for `db:mysql` were indistinguishable here, and the only way to tell
-- them apart was to look at which module it was.
--
-- It had no version, so nothing could be compared against `db:postgresql:>=16` — a
-- requirement is a sentence about a class, a piece of software and a number, and two
-- columns have nowhere to put the number.
--
-- And it could only be given by the core creating one. There was nowhere to write down a
-- database an administrator already had — on another host, inside another network, reached
-- with credentials that are not ours to create — which is the answer to a great many
-- questions a module asks, and the one that was missing.
--
-- One resource to one module. That is the rule and it is deliberate: two modules sharing a
-- database means two modules deciding what is in it, and the failure is not that one of them
-- writes a table the other wanted — it is that nobody can say afterwards whose `users` table
-- was there first. An administrator who wants two modules on one host gets two resources on
-- it, which costs nothing a resource costs.
--
-- The credentials are sealed, because this table is the place they would otherwise pile up
-- and a table is exactly what ends up in a backup. See internal/secrets.
CREATE TABLE IF NOT EXISTS resources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- What kind of thing this is, in the module's own words: `db`, `s3`, whatever kinds
    -- exist. Lower case, because it is compared against what a module asked for and a
    -- comparison that fails on case is a comparison that fails.
    kind TEXT NOT NULL DEFAULT '',

    -- The software behind it and its version: postgresql, 19.1. Together with the kind
    -- these make the address a requirement is written against — `db:postgresql:19.1` — and
    -- they are recorded rather than looked up, because what matters is what this resource
    -- *is*, not what it would be found to be after an upgrade that has not happened yet.
    software TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',

    -- The name an administrator calls it by. The only name a human uses, and nothing
    -- resolves through it: two may share one.
    name TEXT NOT NULL DEFAULT '',

    -- How it came to be here. `managed` is one this instance created; `manual` is one an
    -- administrator described, on a host we do not run. Not decoration: a managed resource
    -- can be dropped by this instance because this instance made it, and a manual one cannot
    -- be dropped at all, only forgotten — and the page says which is which so that nobody
    -- tries.
    origin TEXT NOT NULL DEFAULT 'managed',

    -- The address and the secret, sealed. The address is sealed with the secret because it
    -- carries the password in most forms a database address takes, and a reader who has
    -- one has the other.
    address BYTEA NOT NULL DEFAULT ''::BYTEA,

    -- The module holding it. Null means nobody: the resource was given and given up, or was
    -- never given, and it is on the page to be looked at and deleted.
    integration_id UUID REFERENCES integrations(id) ON DELETE SET NULL,

    -- When it was given up, and to whom it belonged before. Kept after the link is gone,
    -- because "which module used this" is the question somebody asks about an orphan, and
    -- the answer is otherwise lost the moment the module is removed.
    released_at TIMESTAMPTZ,
    last_integration_id UUID,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One resource to one module, which is the rule above. A partial unique index rather than a
-- constraint on the column, so that any number of resources may be free at once.
CREATE UNIQUE INDEX IF NOT EXISTS resources_one_per_module
    ON resources (integration_id)
    WHERE integration_id IS NOT NULL;

-- The page reads orphans — resources nobody holds — and it reads them by this. Without it
-- that page is a sequential scan of a table that grows every time a module is reinstalled.
CREATE INDEX IF NOT EXISTS resources_free_idx
    ON resources (created_at DESC)
    WHERE integration_id IS NULL;