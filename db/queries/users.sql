-- Usuários são globais (um usuário pertence a várias organizações); a tabela
-- não tem RLS.

-- name: CreateUser :one
INSERT INTO users (email, google_sub, apple_sub, password_hash, display_name, email_verified_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 AND deleted_at IS NULL;

-- name: GetUserByGoogleSub :one
SELECT * FROM users WHERE google_sub = $1 AND deleted_at IS NULL;

-- name: GetUserByAppleSub :one
SELECT * FROM users WHERE apple_sub = $1 AND deleted_at IS NULL;

-- name: SetUserEmailVerified :exec
UPDATE users SET email_verified_at = now() WHERE id = $1 AND email_verified_at IS NULL;

-- name: SetUserPasswordHash :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: SetUserDisplayName :exec
UPDATE users SET display_name = $2 WHERE id = $1;

-- name: LinkUserGoogleSub :exec
UPDATE users SET google_sub = $2 WHERE id = $1;

-- name: LinkUserAppleSub :exec
UPDATE users SET apple_sub = $2 WHERE id = $1;

-- name: TouchUserLogin :exec
UPDATE users SET last_login_at = now() WHERE id = $1;

-- name: SoftDeleteUser :execrows
UPDATE users SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: CreateMagicLink :one
INSERT INTO magic_links (email, token_hash, purpose, ip_prefix, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ConsumeMagicLink :one
UPDATE magic_links SET consumed_at = now()
WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING *;

-- name: CountRecentMagicLinks :one
SELECT count(*) FROM magic_links WHERE email = $1 AND created_at > $2;

-- name: DeleteExpiredMagicLinks :execrows
DELETE FROM magic_links WHERE expires_at < now() - interval '7 days';
