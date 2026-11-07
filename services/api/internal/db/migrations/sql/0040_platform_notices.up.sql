-- Public opt-in notices are separate from accounts, subscriptions and marketing preferences.
CREATE TABLE platform_notices (
 id uuid PRIMARY KEY,
 platform text NOT NULL CHECK(platform IN ('android','ios','github','chrome','firefox','safari')),
 email citext NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','confirmed','unsubscribed')),
 confirmation_hash bytea CHECK(confirmation_hash IS NULL OR octet_length(confirmation_hash)=32),
 confirmation_expires_at timestamptz,
 confirmation_outbox uuid REFERENCES outbox(id) ON DELETE SET NULL,
 consent_version text NOT NULL,
 confirmed_at timestamptz,
 unsubscribed_at timestamptz,
 notification_queued_at timestamptz,
 notification_outbox uuid REFERENCES outbox(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 UNIQUE(platform,email)
);
CREATE INDEX platform_notices_expiry ON platform_notices(expires_at);
CREATE INDEX platform_notices_ready ON platform_notices(platform,created_at,id) WHERE status='confirmed' AND notification_queued_at IS NULL;
CREATE UNIQUE INDEX platform_notices_confirmation ON platform_notices(confirmation_hash) WHERE confirmation_hash IS NOT NULL;
CREATE TABLE platform_notice_sends (
 id uuid PRIMARY KEY,
 email_hash bytea NOT NULL CHECK(octet_length(email_hash)=32),
 ip_hash bytea CHECK(ip_hash IS NULL OR octet_length(ip_hash)=32),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX platform_notice_sends_time ON platform_notice_sends(created_at);
CREATE INDEX platform_notice_sends_email ON platform_notice_sends(email_hash,created_at);
CREATE INDEX platform_notice_sends_ip ON platform_notice_sends(ip_hash,created_at);
DO $$ DECLARE tab text; BEGIN
 FOREACH tab IN ARRAY ARRAY['platform_notices','platform_notice_sends'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',tab);
  EXECUTE format('CREATE POLICY system_only ON %I USING(app_is_system()) WITH CHECK(app_is_system())',tab);
  EXECUTE format('GRANT SELECT,INSERT,UPDATE,DELETE ON %I TO %I',tab,app_role_name());
 END LOOP;
END $$;
