-- Exportações de dados pessoais (internal/account). As consultas de estado
-- não carregam o arquivo; só AuthExportArchive o lê.

-- name: AuthInsertExport :one
INSERT INTO account_exports (user_id, org_id)
VALUES ($1, $2)
RETURNING id, user_id, org_id, status, requested_at, ready_at, expires_at, size_bytes, error;

-- name: AuthLatestExport :one
SELECT id, user_id, org_id, status, requested_at, ready_at, expires_at, size_bytes, error
FROM account_exports
WHERE user_id = $1
ORDER BY requested_at DESC
LIMIT 1;

-- name: AuthExportByID :one
SELECT id, user_id, org_id, status, requested_at, ready_at, expires_at, size_bytes, error
FROM account_exports
WHERE id = $1;

-- name: AuthExportArchive :one
SELECT archive FROM account_exports
WHERE id = $1 AND status = 'ready' AND expires_at > now();

-- name: AuthMarkExportReady :execrows
UPDATE account_exports
SET status = 'ready', ready_at = now(), expires_at = $2, archive = $3, size_bytes = $4, error = NULL
WHERE id = $1 AND status = 'pending';

-- name: AuthMarkExportFailed :execrows
UPDATE account_exports SET status = 'failed', error = $2 WHERE id = $1 AND status = 'pending';

-- name: AuthExpireExports :execrows
UPDATE account_exports SET status = 'expired', archive = NULL WHERE status = 'ready' AND expires_at <= $1;

-- name: AuthDeleteOldExports :execrows
DELETE FROM account_exports WHERE requested_at < $1;
