-- Trilha de auditoria encadeada por hash. internal/audit calcula o hash em Go
-- sobre uma forma canônica que inclui meta::text (a serialização do jsonb é
-- determinística), faz o append sob advisory lock e verifica recomputando.

-- name: GetAuditChainTail :one
SELECT seq, hash FROM audit_log WHERE chain_id = $1 ORDER BY seq DESC LIMIT 1;

-- name: AppendAudit :one
INSERT INTO audit_log (org_id, seq, actor_type, actor_id, action, target_type, target_id, meta, prev_hash, hash, request_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: ListAuditRange :many
SELECT id, org_id, chain_id, seq, actor_type, actor_id, action, target_type, target_id, meta::text AS meta_text, prev_hash, hash, request_id, created_at
FROM audit_log
WHERE chain_id = $1 AND seq >= $2 AND seq <= $3
ORDER BY seq ASC;

-- name: ListAuditByOrg :many
SELECT * FROM audit_log WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2;

-- Forma canônica de um jsonb (chaves ordenadas, sem espaços redundantes).
-- name: CanonicalJSON :one
SELECT $1::jsonb::text AS canonical;
