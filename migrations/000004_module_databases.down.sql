ALTER TABLE integrations
    DROP COLUMN IF EXISTS database_name,
    DROP COLUMN IF EXISTS database_role;