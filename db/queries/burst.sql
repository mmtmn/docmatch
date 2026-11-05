-- Pool de trabalhadores sob demanda (docs/escala.md). Tudo em modo sistema: o worker principal
-- enfileira e lê, a API entrega e grava os eventos, o escalonador mede o pool.

-- name: InsertBurstJob :one
INSERT INTO burst_jobs (id, org_id, run_id, state, request, deadline_seconds, created_at)
VALUES ($1, $2, $3, 'queued', $4, $5, $6)
RETURNING *;

-- name: GetBurstJob :one
SELECT * FROM burst_jobs WHERE id = $1;

-- name: GetLiveBurstJobForRun :one
SELECT * FROM burst_jobs WHERE run_id = $1 AND state IN ('queued', 'claimed') LIMIT 1;

-- O mais antigo na fila vai para quem pediu; SKIP LOCKED deixa dois pedidos simultâneos com
-- trabalhos diferentes.
-- name: ClaimBurstJob :one
UPDATE burst_jobs
SET state = 'claimed', instance_id = sqlc.arg(instance_id), token_hash = sqlc.arg(token_hash),
    claimed_at = sqlc.arg(now), lease_expires_at = sqlc.arg(lease_until)
WHERE id = (
  SELECT q.id FROM burst_jobs q WHERE q.state = 'queued' ORDER BY q.created_at LIMIT 1 FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ExtendBurstLease :execrows
UPDATE burst_jobs SET lease_expires_at = sqlc.arg(lease_until)
WHERE id = sqlc.arg(id) AND state = 'claimed' AND token_hash = sqlc.arg(token_hash);

-- name: FinishBurstJob :execrows
UPDATE burst_jobs SET state = sqlc.arg(state), reason = sqlc.arg(reason), finished_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND state = 'claimed' AND token_hash = sqlc.arg(token_hash);

-- name: AbandonBurstJob :execrows
UPDATE burst_jobs SET state = 'abandoned', reason = 'nenhum trabalhador a tempo', finished_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND state = 'queued';

-- name: CancelBurstJob :execrows
UPDATE burst_jobs SET state = 'cancelled', reason = sqlc.arg(reason), finished_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND state IN ('queued', 'claimed');

-- name: ExpireBurstLeases :many
UPDATE burst_jobs SET state = 'lost', reason = 'concessão expirada', finished_at = sqlc.arg(now)
WHERE state = 'claimed' AND lease_expires_at < sqlc.arg(now)
RETURNING id;

-- Um evento por linha, todos do lote na mesma transação; um seq já gravado é ignorado.
-- name: InsertBurstEvent :exec
INSERT INTO burst_events (job_id, org_id, seq, event)
VALUES ($1, $2, $3, $4)
ON CONFLICT (job_id, seq) DO NOTHING;

-- name: ListBurstEvents :many
SELECT seq, event FROM burst_events
WHERE job_id = $1 AND seq > $2
ORDER BY seq
LIMIT $3;

-- name: TouchBurstInstance :exec
INSERT INTO burst_instances (instance_id, instance_type, lifecycle, first_seen_at, last_seen_at)
VALUES (sqlc.arg(instance_id), sqlc.arg(instance_type), sqlc.arg(lifecycle), sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (instance_id) DO UPDATE SET
  last_seen_at = GREATEST(burst_instances.last_seen_at, EXCLUDED.last_seen_at),
  instance_type = CASE WHEN EXCLUDED.instance_type <> '' THEN EXCLUDED.instance_type ELSE burst_instances.instance_type END,
  lifecycle = CASE WHEN EXCLUDED.lifecycle <> '' THEN EXCLUDED.lifecycle ELSE burst_instances.lifecycle END;

-- name: CountBurstJobs :one
SELECT count(*) FILTER (WHERE state = 'queued')::bigint AS queued,
       count(*) FILTER (WHERE state = 'claimed')::bigint AS busy
FROM burst_jobs WHERE state IN ('queued', 'claimed');

-- Instâncias vivas (ouvidas desde live_since) e as que delas estão sem trabalho.
-- name: CountLiveBurstInstances :one
SELECT count(*)::bigint AS live,
       count(*) FILTER (WHERE NOT EXISTS (
         SELECT 1 FROM burst_jobs j WHERE j.state = 'claimed' AND j.instance_id = i.instance_id
       ))::bigint AS idle
FROM burst_instances i WHERE i.last_seen_at >= sqlc.arg(live_since);

-- Segundos de instância do mês: o trecho de cada instância dentro de [month_start, now] mais o boot
-- das que nasceram nele.
-- name: BurstUsageSeconds :one
SELECT COALESCE(sum(
  GREATEST(EXTRACT(EPOCH FROM (LEAST(last_seen_at, sqlc.arg(now)::timestamptz) - GREATEST(first_seen_at, sqlc.arg(month_start)::timestamptz))), 0)
  + CASE WHEN first_seen_at >= sqlc.arg(month_start)::timestamptz THEN sqlc.arg(boot_seconds)::double precision ELSE 0 END
), 0)::double precision AS seconds
FROM burst_instances WHERE last_seen_at >= sqlc.arg(month_start)::timestamptz;

-- name: ListBusyBurstInstances :many
SELECT DISTINCT instance_id::text FROM burst_jobs WHERE state = 'claimed' ORDER BY 1;

-- name: GetBurstPool :one
SELECT failed_claims, unhealthy_until FROM burst_pool WHERE id = 1;

-- name: ResetBurstPool :exec
UPDATE burst_pool SET failed_claims = 0, unhealthy_until = NULL WHERE id = 1 AND (failed_claims <> 0 OR unhealthy_until IS NOT NULL);

-- Uma falta de trabalhador a mais; no limiar o pool entra em espera e a contagem recomeça.
-- name: NoteBurstNoClaim :exec
UPDATE burst_pool SET
  failed_claims = CASE WHEN failed_claims + 1 >= sqlc.arg(threshold)::int THEN 0 ELSE failed_claims + 1 END,
  unhealthy_until = CASE WHEN failed_claims + 1 >= sqlc.arg(threshold)::int THEN sqlc.arg(cooldown_until)::timestamptz ELSE unhealthy_until END
WHERE id = 1;

-- name: PruneBurstJobs :execrows
DELETE FROM burst_jobs WHERE finished_at IS NOT NULL AND finished_at < $1 AND state NOT IN ('queued', 'claimed');
