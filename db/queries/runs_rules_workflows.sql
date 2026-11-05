-- Fluxos de trabalho e regras versionadas (busca por trigrama em título e corpo).

-- name: InsertWorkflow :one
INSERT INTO workflows (org_id, name, description, config, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetWorkflow :one
SELECT * FROM workflows WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: ListWorkflowsPage :many
SELECT * FROM workflows WHERE org_id = $1 AND deleted_at IS NULL ORDER BY name, id LIMIT $2 OFFSET $3;

-- name: UpdateWorkflowConfig :one
UPDATE workflows SET name = $3, config = $4
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: InsertRuleFull :one
INSERT INTO rules (org_id, title, body, kind, source, created_by, version, status, scope, applies_to, compiled, provenance, run_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: ListRulesByIDs :many
SELECT * FROM rules WHERE org_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]) AND deleted_at IS NULL;

-- Sem termo: as usadas recentemente primeiro (lista "já usadas" do seletor).
-- Com termo: trigrama em título e corpo, mais parecidas primeiro.
-- name: SearchRulesPage :many
SELECT * FROM rules
WHERE org_id = $1 AND deleted_at IS NULL
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(scope)::text IS NULL OR scope = sqlc.narg(scope)::text)
  AND (sqlc.narg(term)::text IS NULL
       OR title % sqlc.narg(term)::text OR body % sqlc.narg(term)::text
       OR title ILIKE '%' || sqlc.narg(term)::text || '%' OR body ILIKE '%' || sqlc.narg(term)::text || '%')
ORDER BY CASE WHEN sqlc.narg(term)::text IS NULL THEN 0::real
              ELSE -greatest(similarity(title, sqlc.narg(term)::text), similarity(body, sqlc.narg(term)::text)) END,
         last_used_at DESC NULLS LAST, created_at DESC, id
LIMIT $2 OFFSET $3;

-- name: ListTrustedRules :many
SELECT * FROM rules
WHERE org_id = $1 AND deleted_at IS NULL AND enabled AND status = 'trusted'
ORDER BY created_at, id;

-- name: UpdateRuleVersion :one
UPDATE rules SET title = $3, body = $4, version = $5, status = $6, compiled = $7
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: PromoteRule :one
UPDATE rules SET status = $3, promoted_at = now()
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: InsertRuleVersion :one
INSERT INTO rule_versions (org_id, rule_id, version, title, body, compiled, status, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: TouchRules :exec
UPDATE rules SET last_used_at = now(), use_count = use_count + 1
WHERE org_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]);
