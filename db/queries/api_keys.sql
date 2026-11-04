-- Chaves de API. GetAPIKey roda antes de conhecer a organização (é a própria
-- autenticação), portanto em modo sistema; o resto dentro de WithOrgTx.

-- name: CreateAPIKey :one
INSERT INTO api_keys (org_id, label, prefix, key_hash, scopes, lanes, rate_limit_per_min, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetAPIKey :one
SELECT * FROM api_keys
WHERE id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now());

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = now() WHERE id = $1;

-- name: ListAPIKeys :many
SELECT * FROM api_keys WHERE org_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC;

-- name: RevokeAPIKey :execrows
UPDATE api_keys SET revoked_at = now() WHERE org_id = $1 AND id = $2 AND revoked_at IS NULL;

-- name: CountActiveAPIKeys :one
SELECT count(*) FROM api_keys WHERE org_id = $1 AND revoked_at IS NULL;

-- name: InsertAPIUsage :exec
INSERT INTO api_usage (org_id, api_key_id, route, method, status, lane, credits, duration_ms, request_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListAPIUsage :many
SELECT * FROM api_usage WHERE org_id = $1 AND created_at >= $2 ORDER BY created_at DESC LIMIT $3;
