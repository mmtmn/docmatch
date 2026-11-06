-- A learned rule remains attached to its project even after its source run expires.
ALTER TABLE rules ADD COLUMN project_id uuid;
ALTER TABLE rules ADD CONSTRAINT rules_project_fk FOREIGN KEY(org_id,project_id) REFERENCES projects(org_id,id);
UPDATE rules r SET project_id=pr.project_id FROM project_runs pr WHERE pr.org_id=r.org_id AND pr.run_id=r.run_id;
ALTER TABLE rules ADD CONSTRAINT rules_private_scope CHECK(project_id IS NULL OR scope='tenant');
CREATE INDEX rules_project ON rules(org_id,project_id) WHERE deleted_at IS NULL;
