ALTER TABLE credit_accounts DROP COLUMN billing_blocked;
ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_status_check;
UPDATE subscriptions SET status = 'canceled' WHERE status = 'incomplete_expired';
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_status_check
  CHECK (status IN ('trialing', 'active', 'past_due', 'canceled', 'unpaid', 'incomplete', 'paused'));
ALTER TABLE subscriptions DROP COLUMN livemode;
DROP TABLE billing_orders;
DROP TABLE billing_accounts;
