-- Sessões: consultas quentes por chave primária (cookie) e por refresh_hash
-- (rotação). A tabela é do usuário e não tem RLS.

-- name: CreateSession :one
INSERT INTO sessions (user_id, org_id, device_hash, refresh_hash, family_id, ip_prefix, user_agent, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1 AND revoked_at IS NULL AND expires_at > now();

-- name: GetSessionByRefreshHash :one
SELECT * FROM sessions WHERE refresh_hash = $1;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now() WHERE id = $1;

-- name: RotateSession :one
UPDATE sessions SET refresh_hash = $2, expires_at = $3, last_seen_at = now()
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: SetSessionOrg :exec
UPDATE sessions SET org_id = $2 WHERE id = $1;

-- name: RevokeSession :execrows
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeSessionFamily :execrows
UPDATE sessions SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeUserSessions :execrows
UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: ListActiveSessions :many
SELECT * FROM sessions
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now() - interval '30 days';
