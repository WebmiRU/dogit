-- One token per module, and the token is what it authenticates with.
--
-- Until now a module held two credentials: the one an administrator registered it with, which
-- the core checked once and then forgot, and a second one the core handed back at that moment
-- and the module used for everything afterwards. Two secrets to place, to store, to rotate and
-- to revoke, where the first is spent on first use — and an operator who revoked the token they
-- created believed the module had been locked out while it carried on working on the other.
--
-- So the token an administrator created becomes the module's credential: presented at
-- registration, bound to the module that presented it, and the only thing authenticating it from
-- then on. GitLab's arrangement, and for the same reason — there is nothing else to lose, and
-- nothing to keep in step with anything else.
--
-- `expires_at` is why the credential has to live here rather than on the module's row. A token
-- with an end must actually end, and a core that read the date only at registration would keep
-- a module running on an expired token until the day it happened to restart — which is most of
-- the time.
ALTER TABLE module_tokens
    ADD COLUMN expires_at TIMESTAMPTZ,
    ADD COLUMN integration_id UUID REFERENCES integrations(id) ON DELETE CASCADE;

-- Every running module is holding a credential that is on no row here, and it has to be given
-- one before the column it is in can go.
--
-- The secret does not change in this step — only where it is kept — so nothing has to be
-- reinstalled and no module is interrupted. The rows are named after the module they belong to
-- rather than after whoever minted them, because from here on that is what a token row means:
-- not "an invitation somebody may use", but "the credential of this module, and this is its end
-- if it has one".
--
-- The registration tokens already in this table are left alone rather than tidied up. They were
-- spent the moment their module registered, and a spent token is an invitation that has not been
-- used again — which is still an invitation, and quietly deleting an administrator's rows during
-- a migration is not this migration's business.
--
-- Written as an upsert because this has to be safe to apply twice. Rolling back puts the secret
-- back on the module's row but leaves the token row alone, so re-applying finds a row already
-- holding that hash — and there is a unique index on the hash, which is the right thing to have
-- and turns a plain INSERT into a failure at exactly the moment somebody is trying to recover.
-- Where a row is already there, it is bound rather than duplicated, and its name and description
-- are left as they are: those are an administrator's, not this migration's to overwrite.
INSERT INTO module_tokens (name, description, token_hash, integration_id)
SELECT 'carried over from ' || i.kind || '/' || i.name,
       'The credential this module was already working with, from when modules held two.',
       i.token_hash, i.id
  FROM integrations i
 WHERE i.token_hash IS NOT NULL
ON CONFLICT (token_hash) DO UPDATE SET integration_id = EXCLUDED.integration_id;

-- At most one credential per module. Without it an administrator could mint a second token for a
-- module that already has one, and the module's identity would be whichever of the two happened
-- to be presented first — the same "one resource per kind" rule the plan is built on, one level
-- down.
CREATE UNIQUE INDEX module_tokens_one_per_module
    ON module_tokens (integration_id)
    WHERE integration_id IS NOT NULL;

-- Every module request resolves a secret to a module through this lookup: the hash index finds
-- the row, and the row says which module it belongs to.
CREATE INDEX module_tokens_by_module_idx ON module_tokens (integration_id);

-- The module's own row no longer carries the secret. Everything that answers "which module is
-- this token" now answers from this table, and a second copy of the same hash on a row with no
-- expiry, no name and no module on it is only a second place for the two to disagree.
ALTER TABLE integrations DROP COLUMN IF EXISTS token_hash;
