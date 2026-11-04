-- Regras salvas: busca por trigrama no título (índice GIN) ordenada por
-- similaridade e uso recente.

-- name: CreateRule :one
INSERT INTO rules (org_id, title, body, compiled_sql, kind, source, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetRule :one
SELECT * FROM rules WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: SearchRules :many
SELECT sqlc.embed(rules), similarity(rules.title, sqlc.arg(term)::text)::real AS score
FROM rules
WHERE rules.org_id = $1 AND rules.deleted_at IS NULL
  AND (rules.title % sqlc.arg(term)::text OR rules.title ILIKE '%' || sqlc.arg(term)::text || '%')
ORDER BY similarity(rules.title, sqlc.arg(term)::text) DESC, rules.last_used_at DESC NULLS LAST
LIMIT sqlc.arg(max_rows);

-- name: ListRecentlyUsedRules :many
SELECT * FROM rules
WHERE org_id = $1 AND deleted_at IS NULL
ORDER BY last_used_at DESC NULLS LAST, created_at DESC
LIMIT $2;

-- name: TouchRule :exec
UPDATE rules SET last_used_at = now(), use_count = use_count + 1 WHERE org_id = $1 AND id = $2;

-- name: UpdateRule :one
UPDATE rules SET title = $3, body = $4, compiled_sql = $5, enabled = $6
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteRule :execrows
UPDATE rules SET deleted_at = now() WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: InsertRuleUse :one
INSERT INTO rule_uses (org_id, rule_id, run_id, matched, false_positives, amount_explained)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListRuleUses :many
SELECT * FROM rule_uses WHERE org_id = $1 AND rule_id = $2 ORDER BY created_at DESC LIMIT $3;
