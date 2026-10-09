-- Record the upstream repository for forks so project lists can show a real fork count.
ALTER TABLE projects
    ADD COLUMN forked_from_project_id UUID REFERENCES projects(id) ON DELETE SET NULL;

CREATE INDEX projects_forked_from_project_id_idx
    ON projects (forked_from_project_id);
