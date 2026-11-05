-- Outbox transacional: a aplicação enfileira na mesma transação do dado; o
-- worker (modo sistema) reivindica com SKIP LOCKED e marca como enviado.

-- name: EnqueueOutbox :one
INSERT INTO outbox (org_id, kind, payload, available_at)
VALUES ($1, $2, $3, coalesce(sqlc.narg(available_at)::timestamptz, now()))
RETURNING *;

-- Kinds owned by a dedicated worker (e-mail) are skipped: claiming them here
-- would mark them sent without delivering.
-- name: ClaimOutbox :many
UPDATE outbox SET attempts = attempts + 1
WHERE id IN (
  SELECT id FROM outbox
  WHERE sent_at IS NULL AND available_at <= now()
    AND NOT (kind = ANY(sqlc.arg(skip_kinds)::text[]))
  ORDER BY available_at
  LIMIT sqlc.arg(max_rows)
  FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkOutboxSent :exec
UPDATE outbox SET sent_at = now(), last_error = NULL WHERE id = $1;

-- name: RetryOutboxLater :exec
UPDATE outbox SET available_at = $2, last_error = $3 WHERE id = $1;

-- name: CountUnsentOutbox :one
SELECT count(*) FROM outbox WHERE sent_at IS NULL;

-- name: DeleteSentOutbox :execrows
DELETE FROM outbox WHERE sent_at IS NOT NULL AND sent_at < $1;
