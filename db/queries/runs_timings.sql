-- Durações das execuções terminadas (tempo esperado dos orçamentos) e a fila
-- de execuções que o estimador conta para a espera.

-- name: InsertRunTiming :exec
INSERT INTO run_timings (run_id, org_id, status, units, active_seconds, expected_seconds, finished_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (run_id) DO NOTHING;

-- Concluídas recentes de todas as contas (modo sistema), mais novas primeiro.
-- name: ListRecentRunTimings :many
SELECT units, active_seconds FROM run_timings
WHERE status = 'completed' AND finished_at > $1
ORDER BY finished_at DESC
LIMIT $2;

-- Execuções na frente de uma nova (modo sistema): em fila ou rodando.
-- name: CountRunsInFlight :one
SELECT count(*) FROM runs WHERE status IN ('queued', 'running');
