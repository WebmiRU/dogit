DROP INDEX IF EXISTS docker_registry_credentials_scope_idx;
DROP TABLE IF EXISTS docker_registry_credentials;

ALTER TABLE docker_registries DROP CONSTRAINT IF EXISTS docker_registries_credential_source;
ALTER TABLE docker_registries DROP COLUMN IF EXISTS credential_source;
