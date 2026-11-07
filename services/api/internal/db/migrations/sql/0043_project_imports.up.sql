-- The baseline detects edits after import; it is not an invented rule snapshot
-- for a historical run. Original inputs and results remain immutable.
CREATE TABLE project_imports (
 org_id uuid NOT NULL,
 project_id uuid NOT NULL,
 run_id uuid NOT NULL,
 baseline_signature text CHECK(baseline_signature IS NULL OR length(baseline_signature)=64),
 PRIMARY KEY(org_id,project_id),
 UNIQUE(org_id,run_id),
 FOREIGN KEY(org_id,project_id) REFERENCES projects(org_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,run_id) REFERENCES runs(org_id,id) ON DELETE CASCADE
);
ALTER TABLE project_imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_imports FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON project_imports USING(org_id=app_org_id() OR app_is_system()) WITH CHECK(org_id=app_org_id() OR app_is_system());
DO $$ BEGIN EXECUTE format('GRANT SELECT,INSERT,UPDATE ON project_imports TO %I',app_role_name()); END $$;
