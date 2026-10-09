DROP INDEX IF EXISTS projects_forked_from_project_id_idx;
ALTER TABLE projects DROP COLUMN IF EXISTS forked_from_project_id;
