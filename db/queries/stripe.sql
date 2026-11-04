-- Eventos Stripe: a chave primária é o id do evento. InsertStripeEvent não
-- devolve linha quando o evento já existe (pgx.ErrNoRows = duplicado).

-- name: InsertStripeEvent :one
INSERT INTO stripe_events (id, type, api_version, payload)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO NOTHING
RETURNING id;

-- name: GetStripeEvent :one
SELECT * FROM stripe_events WHERE id = $1;

-- name: MarkStripeEventProcessed :exec
UPDATE stripe_events SET processed_at = now(), error = NULL WHERE id = $1;

-- name: MarkStripeEventFailed :exec
UPDATE stripe_events SET error = $2 WHERE id = $1;

-- name: ListUnprocessedStripeEvents :many
SELECT * FROM stripe_events WHERE processed_at IS NULL ORDER BY received_at LIMIT $1;

-- name: UpsertSubscription :one
INSERT INTO subscriptions (org_id, plan_code, stripe_customer_id, stripe_subscription_id, status, current_period_start, current_period_end, cancel_at, canceled_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (stripe_subscription_id) DO UPDATE SET
  plan_code = EXCLUDED.plan_code,
  status = EXCLUDED.status,
  current_period_start = EXCLUDED.current_period_start,
  current_period_end = EXCLUDED.current_period_end,
  cancel_at = EXCLUDED.cancel_at,
  canceled_at = EXCLUDED.canceled_at
RETURNING *;

-- name: GetActiveSubscription :one
SELECT * FROM subscriptions
WHERE org_id = $1 AND status IN ('trialing', 'active', 'past_due')
ORDER BY created_at DESC
LIMIT 1;
