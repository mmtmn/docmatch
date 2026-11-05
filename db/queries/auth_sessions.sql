-- Sessões (internal/session): o cookie dm_session carrega "id.segredo", a
-- busca quente é por chave primária e a rotação do refresh é feita no lugar.
-- A tabela é do usuário (existe antes da organização) e não tem RLS.

-- name: AuthInsertSession :one
INSERT INTO sessions (id, user_id, org_id, device_hash, refresh_hash, access_hash, family_id, ip_prefix, user_agent, expires_at, access_expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- Devolve a linha em qualquer estado: o chamador distingue revogada, expirada
-- e reuso de refresh para decidir entre 401 e revogar a família.
-- name: AuthSessionByID :one
SELECT * FROM sessions WHERE id = $1;

-- name: AuthRotateSession :one
UPDATE sessions
SET refresh_hash = $2, access_hash = $3, expires_at = $4, access_expires_at = $5,
    last_seen_at = now(), rotated_at = now(), rotations = rotations + 1
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: AuthTouchSession :exec
UPDATE sessions SET last_seen_at = now() WHERE id = $1 AND last_seen_at < now() - interval '1 minute';

-- name: AuthRevokeSession :execrows
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: AuthRevokeSessionFamily :execrows
UPDATE sessions SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL;

-- Revoga todas as sessões do usuário, exceto (opcionalmente) a atual.
-- name: AuthRevokeUserSessions :execrows
UPDATE sessions SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL
  AND (sqlc.narg(keep)::uuid IS NULL OR id <> sqlc.narg(keep)::uuid);

-- name: AuthListUserSessions :many
SELECT * FROM sessions
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: AuthDeleteStaleSessions :execrows
DELETE FROM sessions
WHERE expires_at < $1 OR (revoked_at IS NOT NULL AND revoked_at < $1);
