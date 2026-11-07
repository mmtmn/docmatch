-- Login grants are identity-scoped secrets, never tenant data or export data.
-- Deletion detaches them from the user and leaves only an encrypted, bounded retry.
CREATE TABLE apple_credentials (
    id uuid PRIMARY KEY,
    user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    client_id text NOT NULL CHECK (length(client_id) BETWEEN 1 AND 255),
    sealed_token bytea NOT NULL CHECK (octet_length(sealed_token) BETWEEN 28 AND 16412),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz,
    expires_at timestamptz,
    UNIQUE (user_id, client_id)
);
CREATE INDEX apple_credentials_pending ON apple_credentials(next_attempt_at) WHERE user_id IS NULL;

ALTER TABLE apple_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE apple_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY system_only ON apple_credentials USING(app_is_system()) WITH CHECK(app_is_system());
DO $$ BEGIN
 EXECUTE format('GRANT SELECT,INSERT,UPDATE,DELETE ON apple_credentials TO %I',app_role_name());
END $$;
