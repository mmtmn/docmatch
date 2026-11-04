-- Usuários e organizações para o login (internal/auth) e a conta
-- (internal/account). users não tem RLS; orgs e memberships têm, portanto as
-- consultas que as tocam rodam em WithOrgTx (ou em modo sistema no worker).

-- name: AuthSetUserNotifications :exec
UPDATE users SET notifications = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: AuthSetUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: AuthUpdateOrgName :exec
UPDATE orgs SET name = $2 WHERE id = $1 AND deleted_at IS NULL;

-- Agenda a exclusão (LGPD); devolve zero linhas quando já havia um pedido.
-- name: AuthRequestUserDeletion :one
UPDATE users SET deletion_requested_at = now(), deletion_scheduled_for = $2
WHERE id = $1 AND deleted_at IS NULL AND deletion_requested_at IS NULL
RETURNING *;

-- name: AuthListUsersDueForErasure :many
SELECT id FROM users
WHERE deleted_at IS NULL AND deletion_scheduled_for IS NOT NULL AND deletion_scheduled_for <= $1
ORDER BY deletion_scheduled_for
LIMIT $2;

-- name: AuthCountOrgMembers :one
SELECT count(*) FROM memberships WHERE org_id = $1;

-- name: AuthHardDeleteUser :execrows
DELETE FROM users WHERE id = $1;

-- name: AuthHardDeleteOrg :execrows
DELETE FROM orgs WHERE id = $1;
