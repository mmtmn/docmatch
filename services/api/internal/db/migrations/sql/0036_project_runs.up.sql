ALTER TABLE runs ADD CONSTRAINT runs_org_id_unique UNIQUE(org_id,id);
CREATE TABLE project_runs (
 org_id uuid NOT NULL,
 project_id uuid NOT NULL,
 run_id uuid NOT NULL,
 revision bigint NOT NULL,
 signature text NOT NULL CHECK(length(signature)=64),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,run_id),
 FOREIGN KEY(org_id,project_id) REFERENCES projects(org_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,run_id) REFERENCES runs(org_id,id) ON DELETE CASCADE
);
CREATE INDEX project_runs_signature ON project_runs(org_id,project_id,signature,created_at DESC);
ALTER TABLE project_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON project_runs USING(org_id=app_org_id() OR app_is_system()) WITH CHECK(org_id=app_org_id() OR app_is_system());
DO $$ BEGIN EXECUTE format('GRANT SELECT,INSERT ON project_runs TO %I',app_role_name()); END $$;
