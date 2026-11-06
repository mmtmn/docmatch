CREATE TABLE projects (
 id uuid PRIMARY KEY DEFAULT uuidv7(),
 org_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
 owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 120),
 visibility text NOT NULL DEFAULT 'private' CHECK(visibility IN ('private','team')),
 version bigint NOT NULL DEFAULT 1,
 revision bigint NOT NULL DEFAULT 1,
 settings jsonb NOT NULL DEFAULT '{"use_rules":true}'::jsonb CHECK(octet_length(settings::text)<=16384),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 archived_at timestamptz,
 deleted_at timestamptz,
 UNIQUE(org_id,id)
);
CREATE INDEX projects_recent ON projects(org_id,updated_at DESC,id) WHERE deleted_at IS NULL;

CREATE TABLE project_members (
 org_id uuid NOT NULL,
 project_id uuid NOT NULL,
 user_id uuid NOT NULL,
 PRIMARY KEY(org_id,project_id,user_id),
 FOREIGN KEY(org_id,project_id) REFERENCES projects(org_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,user_id) REFERENCES memberships(org_id,user_id) ON DELETE CASCADE
);

ALTER TABLE uploads ADD CONSTRAINT uploads_org_id_unique UNIQUE(org_id,id);
CREATE TABLE project_documents (
 id uuid PRIMARY KEY DEFAULT uuidv7(),
 org_id uuid NOT NULL,
 project_id uuid NOT NULL,
 upload_id uuid NOT NULL,
 replaces_id uuid,
 pending_replacement_id uuid,
 role text NOT NULL DEFAULT '' CHECK(char_length(role)<=80),
 created_by uuid REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 removed_at timestamptz,
 UNIQUE(org_id,upload_id),
 UNIQUE(org_id,project_id,id),
 FOREIGN KEY(org_id,project_id) REFERENCES projects(org_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,upload_id) REFERENCES uploads(org_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,project_id,replaces_id) REFERENCES project_documents(org_id,project_id,id),
 FOREIGN KEY(org_id,project_id,pending_replacement_id) REFERENCES project_documents(org_id,project_id,id)
);
CREATE INDEX project_documents_active ON project_documents(org_id,project_id,created_at) WHERE removed_at IS NULL;
CREATE TABLE project_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 org_id uuid NOT NULL,
 project_id uuid NOT NULL,
 actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
 action text NOT NULL,
 details jsonb NOT NULL DEFAULT '{}'::jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(org_id,project_id) REFERENCES projects(org_id,id) ON DELETE CASCADE
);
CREATE INDEX project_events_recent ON project_events(org_id,project_id,id DESC);

DO $$ DECLARE tab text; BEGIN
 FOREACH tab IN ARRAY ARRAY['projects','project_members','project_documents','project_events'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',tab);
  EXECUTE format('CREATE POLICY tenant_isolation ON %I USING(org_id=app_org_id() OR app_is_system()) WITH CHECK(org_id=app_org_id() OR app_is_system())',tab);
  EXECUTE format('GRANT SELECT,INSERT ON %I TO %I',tab,app_role_name());
 END LOOP;
 EXECUTE format('GRANT UPDATE,DELETE ON projects,project_members,project_documents TO %I',app_role_name());
 EXECUTE format('GRANT USAGE ON SEQUENCE project_events_id_seq TO %I',app_role_name());
END $$;
