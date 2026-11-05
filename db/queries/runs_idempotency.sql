-- Chaves de idempotência das operações que gastam créditos (24 h).

-- name: InsertIdempotencyKey :one
INSERT INTO idempotency_keys (org_id, key, request_hash, expires_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (org_id, key) DO NOTHING
RETURNING *;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE org_id = $1 AND key = $2;

-- name: FinishIdempotencyKey :exec
UPDATE idempotency_keys SET status = 'done', response_status = $3, response_body = $4
WHERE org_id = $1 AND key = $2;

-- Libera a chave quando a operação falhou antes de responder, para o cliente repetir.
-- name: DeleteIdempotencyKey :exec
DELETE FROM idempotency_keys WHERE org_id = $1 AND key = $2;

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE expires_at < $1;
