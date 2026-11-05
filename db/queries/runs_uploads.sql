-- Uploads do pipeline de execuções (ticket pré-assinado, conclusão, veredito
-- do scanner). Consultas por organização rodam dentro de WithOrgTx; as de
-- sistema (veredito vindo da fila) dentro de WithSystemTx.

-- name: InsertUploadTicket :one
INSERT INTO uploads (id, org_id, user_id, kind, store, role, file_name, content_type, size_bytes, sha256, storage_key, status, ticket_expires_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12, $13)
RETURNING *;

-- name: ListUploadsByIDs :many
SELECT * FROM uploads WHERE org_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]) AND deleted_at IS NULL;

-- name: GetUploadSystem :one
SELECT * FROM uploads WHERE id = $1 AND deleted_at IS NULL;

-- name: UpdateUploadState :one
UPDATE uploads
SET status = $3, sha256 = $4, detected_mime = $5, rejected_reason = $6, scan_result = $7,
    scanned_at = $8, completed_at = $9, duration_ms = $10
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING *;
