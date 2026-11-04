-- Organizações. A política de RLS exige app.org_id = id da própria org (ou
-- modo sistema) até para criar: o chamador gera o uuid, abre a transação com
-- WithOrgTx(novoID) e insere.

-- name: CreateOrg :one
INSERT INTO orgs (id, name, slug, plan_code, country)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetOrg :one
SELECT * FROM orgs WHERE id = $1 AND deleted_at IS NULL;

-- name: GetOrgBySlug :one
SELECT * FROM orgs WHERE slug = $1 AND deleted_at IS NULL;

-- name: UpdateOrgPlan :exec
UPDATE orgs SET plan_code = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteOrg :execrows
UPDATE orgs SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: AddMembership :exec
INSERT INTO memberships (org_id, user_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: GetMembership :one
SELECT * FROM memberships WHERE org_id = $1 AND user_id = $2;

-- name: RemoveMembership :execrows
DELETE FROM memberships WHERE org_id = $1 AND user_id = $2;

-- Lista as organizações de um usuário (atravessa locatários: modo sistema).
-- name: ListUserMemberships :many
SELECT m.org_id, m.user_id, m.role, m.created_at, o.name AS org_name, o.slug AS org_slug, o.plan_code
FROM memberships m
JOIN orgs o ON o.id = m.org_id
WHERE m.user_id = $1 AND o.deleted_at IS NULL
ORDER BY o.name;
