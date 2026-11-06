ALTER TABLE customizations ADD COLUMN base_version integer NOT NULL DEFAULT 0;
ALTER TABLE tenant_themes ADD COLUMN endpoints jsonb NOT NULL DEFAULT '[]'::jsonb;
CREATE TABLE workspace_versions (
 org_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
 version integer NOT NULL,
 manifest jsonb NOT NULL,
 endpoints jsonb NOT NULL,
 customization_id uuid REFERENCES customizations(id) ON DELETE SET NULL,
 created_by uuid REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,version)
);
ALTER TABLE workspace_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_versions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON workspace_versions USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id());
DO $$ BEGIN EXECUTE format('GRANT SELECT,INSERT ON workspace_versions TO %I', app_role_name()); END $$;
ALTER TABLE customizations ADD COLUMN request_key uuid;
CREATE UNIQUE INDEX customizations_request_key ON customizations(org_id,request_key);
