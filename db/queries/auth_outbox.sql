-- Envio de e-mail pelo outbox (internal/email): o job imediato do River lê a
-- linha pelo id; o varredor periódico reivindica as linhas antigas ainda não
-- enviadas. Ambos em modo sistema (o outbox não tem RLS).

-- name: AuthOutboxByID :one
SELECT * FROM outbox WHERE id = $1;

-- name: AuthMarkOutboxAttempt :one
UPDATE outbox SET attempts = attempts + 1 WHERE id = $1 AND sent_at IS NULL RETURNING *;

-- name: AuthClaimStaleOutbox :many
UPDATE outbox SET attempts = attempts + 1
WHERE id IN (
  SELECT o.id FROM outbox o
  WHERE o.kind = $1 AND o.sent_at IS NULL AND o.available_at <= $2
  ORDER BY o.available_at
  LIMIT $3
  FOR UPDATE SKIP LOCKED
)
RETURNING *;
