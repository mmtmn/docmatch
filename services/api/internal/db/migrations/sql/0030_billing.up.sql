-- Stripe customer mappings and checkout intents are scoped to the tenant and payment mode.
CREATE TABLE billing_accounts (
  org_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  livemode boolean NOT NULL,
  stripe_customer_id text NOT NULL UNIQUE,
  blocked boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, livemode)
);

CREATE TABLE billing_orders (
  id uuid PRIMARY KEY,
  org_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  livemode boolean NOT NULL,
  idempotency_key text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('subscription', 'topup')),
  plan_code text NOT NULL,
  credits bigint NOT NULL CHECK (credits > 0),
  amount_cents bigint NOT NULL CHECK (amount_cents > 0),
  price_id text NOT NULL,
  stripe_session_id text UNIQUE,
  checkout_url text,
  expires_at timestamptz,
  paid_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, livemode, idempotency_key)
);
CREATE INDEX billing_orders_pending_idx ON billing_orders (org_id, livemode, created_at DESC) WHERE paid_at IS NULL;

ALTER TABLE subscriptions ADD COLUMN livemode boolean NOT NULL DEFAULT false;
ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_status_check;
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_status_check
  CHECK (status IN ('trialing', 'active', 'past_due', 'canceled', 'unpaid', 'incomplete', 'incomplete_expired', 'paused'));
ALTER TABLE credit_accounts ADD COLUMN billing_blocked boolean NOT NULL DEFAULT false;

ALTER TABLE billing_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billing_accounts FOR ALL TO PUBLIC
  USING (org_id = app_org_id() OR app_is_system())
  WITH CHECK (org_id = app_org_id() OR app_is_system());
ALTER TABLE billing_orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_orders FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billing_orders FOR ALL TO PUBLIC
  USING (org_id = app_org_id() OR app_is_system())
  WITH CHECK (org_id = app_org_id() OR app_is_system());
DO $$ BEGIN
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON billing_accounts, billing_orders TO %I', app_role_name());
END $$;
