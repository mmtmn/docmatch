-- Extension credentials are separate from cookie credentials. The linked session
-- lets the existing device list, sign-out and account deletion revoke them.
CREATE TABLE extension_codes (
  code_hash bytea PRIMARY KEY CHECK (octet_length(code_hash)=32),
  client_id text NOT NULL,
  redirect_uri text NOT NULL,
  challenge text NOT NULL,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id uuid REFERENCES orgs(id) ON DELETE SET NULL,
  authorizing_session uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now()+interval '2 minutes',
  consumed_at timestamptz,
  issued_session uuid REFERENCES sessions(id) ON DELETE SET NULL
);
CREATE INDEX extension_codes_expiry ON extension_codes(expires_at);
CREATE INDEX extension_codes_user_created ON extension_codes(user_id,created_at);
CREATE TABLE extension_grants (
  session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  client_id text NOT NULL,
  access_hash bytea NOT NULL CHECK (octet_length(access_hash)=32),
  refresh_hash bytea NOT NULL CHECK (octet_length(refresh_hash)=32),
  previous_refresh_hash bytea,
  refresh_request_id uuid,
  retry_until timestamptz,
  retry_response bytea
);
CREATE INDEX extension_grants_retry ON extension_grants(retry_until) WHERE retry_response IS NOT NULL;
CREATE TABLE extension_spent_refresh (
 token_hash bytea PRIMARY KEY CHECK(octet_length(token_hash)=32),
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 expires_at timestamptz NOT NULL
);
CREATE INDEX extension_spent_expiry ON extension_spent_refresh(expires_at);
CREATE INDEX extension_spent_session ON extension_spent_refresh(session_id);
DO $$ BEGIN
 EXECUTE format('GRANT SELECT,INSERT,UPDATE,DELETE ON extension_codes,extension_grants,extension_spent_refresh TO %I',app_role_name());
END $$;
