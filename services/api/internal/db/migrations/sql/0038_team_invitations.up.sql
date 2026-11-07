CREATE TABLE team_invitations (
 id uuid PRIMARY KEY DEFAULT uuidv7(),
 org_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
 email citext NOT NULL CHECK(char_length(email) BETWEEN 3 AND 254),
 role text NOT NULL CHECK(role IN ('uploader','reconciler')),
 token_hash bytea NOT NULL UNIQUE CHECK(octet_length(token_hash)=32),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','accepted','declined','expired','revoked')),
 invited_by uuid REFERENCES users(id) ON DELETE SET NULL,
 accepted_by uuid REFERENCES users(id) ON DELETE SET NULL,
 expires_at timestamptz NOT NULL,
 sent_at timestamptz NOT NULL DEFAULT now(),
 accepted_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX team_invitation_pending_email ON team_invitations(org_id,email) WHERE status='pending';
CREATE INDEX team_invitations_recent ON team_invitations(org_id,created_at DESC,id);
CREATE INDEX team_invitations_recipient ON team_invitations(email,status);
CREATE TABLE team_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 org_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
 actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
 target_id uuid,
 action text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX team_events_recent ON team_events(org_id,id DESC);
DO $$ DECLARE tab text; BEGIN
 FOREACH tab IN ARRAY ARRAY['team_invitations','team_events'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',tab);
  EXECUTE format('CREATE POLICY tenant_isolation ON %I USING(org_id=app_org_id() OR app_is_system()) WITH CHECK(org_id=app_org_id() OR app_is_system())',tab);
  EXECUTE format('GRANT SELECT,INSERT ON %I TO %I',tab,app_role_name());
 END LOOP;
 EXECUTE format('GRANT UPDATE,DELETE ON team_invitations TO %I',app_role_name());
 EXECUTE format('GRANT USAGE ON SEQUENCE team_events_id_seq TO %I',app_role_name());
END $$;
