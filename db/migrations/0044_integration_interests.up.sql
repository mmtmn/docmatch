-- Product-interest signals only: no credentials, document content or mailing-list enrollment.
CREATE TABLE integration_interests (
 integration_id text NOT NULL CHECK(integration_id IN ('conta-azul','omie','totvs','sap','quickbooks','xero','projuris','astrea','clio','docusign')),
 subject_hash bytea NOT NULL CHECK(octet_length(subject_hash)=32),
 user_id uuid REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(integration_id,subject_hash)
);
CREATE INDEX integration_interests_updated ON integration_interests(updated_at);
ALTER TABLE integration_interests ENABLE ROW LEVEL SECURITY;
ALTER TABLE integration_interests FORCE ROW LEVEL SECURITY;
CREATE POLICY system_only ON integration_interests USING(app_is_system()) WITH CHECK(app_is_system());
DO $$ BEGIN
 EXECUTE format('GRANT SELECT,INSERT,UPDATE,DELETE ON integration_interests TO %I',app_role_name());
END $$;
