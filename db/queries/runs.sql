-- Execuções. Consulta quente: as mais recentes da organização.

-- name: CreateRun :one
INSERT INTO runs (org_id, workflow_id, user_id, api_key_id, quote_id, hold_id, lane, credits_held)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE org_id = $1 AND id = $2;

-- name: ListRecentRuns :many
SELECT * FROM runs WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2;

-- name: ListRecentRunsBefore :many
SELECT * FROM runs WHERE org_id = $1 AND created_at < $2 ORDER BY created_at DESC LIMIT $3;

-- name: CountActiveRuns :one
SELECT count(*) FROM runs WHERE org_id = $1 AND status IN ('queued', 'running');

-- name: StartRun :one
UPDATE runs SET status = 'running', started_at = now()
WHERE org_id = $1 AND id = $2 AND status = 'queued'
RETURNING *;

-- name: FinishRun :one
UPDATE runs SET status = $3, finished_at = now(), error_code = $4, credits_settled = $5, stats = $6
WHERE org_id = $1 AND id = $2 AND status IN ('queued', 'running')
RETURNING *;

-- Próxima execução da fila (modo sistema; o worker define app.org_id depois).
-- name: NextQueuedRun :one
SELECT * FROM runs WHERE status = 'queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED;

-- name: CreateUpload :one
INSERT INTO uploads (org_id, user_id, run_id, file_name, content_type, size_bytes, sha256, storage_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetUpload :one
SELECT * FROM uploads WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: ListRunUploads :many
SELECT * FROM uploads WHERE org_id = $1 AND run_id = $2 AND deleted_at IS NULL ORDER BY created_at;

-- name: SetUploadScan :exec
UPDATE uploads SET status = $3, scan_result = $4, scanned_at = now() WHERE org_id = $1 AND id = $2;

-- name: CountRecentUploads :one
SELECT count(*) FROM uploads WHERE org_id = $1 AND created_at > $2;

-- name: GetRunWithProject :one
SELECT sqlc.embed(r),pr.project_id FROM runs r LEFT JOIN project_runs pr ON pr.org_id=r.org_id AND pr.run_id=r.id
WHERE r.org_id=$1 AND r.id=$2;
