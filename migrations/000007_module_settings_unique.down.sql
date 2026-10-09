-- The stricter constraint cannot be undone: allowing the duplicates the old one
-- permitted is not something a downgrade should offer to do silently.
ALTER TABLE integration_settings
    DROP CONSTRAINT IF EXISTS integration_settings_one_per_scope;
