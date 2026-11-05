-- Pool de trabalhadores sob demanda (docs/escala.md). Tudo em modo sistema: o worker principal
-- enfileira e lê, a API entrega e grava os eventos, o escalonador mede o pool. Concessões, fins e
-- horas usam o relógio do banco (now()), o mesmo para a API e para o worker.

-- A reserva de lugar no pool é atômica: quem cria um trabalho segura este lock até o fim da
-- transação, conta os vivos e só então insere (dois despachos simultâneos nunca passam do teto).
-- name: LockBurstCapacity :exec
SELECT pg_advisory_xact_lock(7262738);

-- name: CountLiveBurstJobs :one
SELECT count(*)::bigint FROM burst_jobs WHERE state IN ('queued', 'claimed');

-- name: InsertBurstJob :one
INSERT INTO burst_jobs (id, org_id, run_id, state, request, deadline_seconds)
VALUES (sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(run_id), 'queued', sqlc.arg(request), sqlc.arg(deadline_seconds))
RETURNING *;

-- name: GetBurstJob :one
SELECT * FROM burst_jobs WHERE id = $1;

-- name: GetLiveBurstJobForRun :one
SELECT * FROM burst_jobs WHERE run_id = $1 AND state IN ('queued', 'claimed') LIMIT 1;

-- O trabalho mais novo da execução, em qualquer estado (reconexão a um que terminou sem ninguém ler).
-- name: GetLatestBurstJobForRun :one
SELECT * FROM burst_jobs WHERE run_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1;

-- O mais antigo na fila vai para quem pediu; SKIP LOCKED deixa dois pedidos simultâneos com
-- trabalhos diferentes.
-- name: ClaimBurstJob :one
UPDATE burst_jobs
SET state = 'claimed', instance_id = sqlc.arg(instance_id), token_hash = sqlc.arg(token_hash),
    claimed_at = now(), lease_expires_at = now() + sqlc.arg(lease_seconds)::int * interval '1 second'
WHERE id = (
  SELECT q.id FROM burst_jobs q WHERE q.state = 'queued' ORDER BY q.created_at LIMIT 1 FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- Uma concessão vencida não revive: só o escalonador ou o despachante a encerram.
-- name: ExtendBurstLease :execrows
UPDATE burst_jobs SET lease_expires_at = now() + sqlc.arg(lease_seconds)::int * interval '1 second'
WHERE id = sqlc.arg(id) AND state = 'claimed' AND token_hash = sqlc.arg(token_hash) AND lease_expires_at >= now();

-- name: FinishBurstJob :execrows
UPDATE burst_jobs SET state = sqlc.arg(state), reason = sqlc.arg(reason), finished_at = now()
WHERE id = sqlc.arg(id) AND state = 'claimed' AND token_hash = sqlc.arg(token_hash);

-- Abandona só um trabalho que ainda está na fila e já esperou wait_seconds pelo relógio do banco.
-- name: AbandonBurstJob :execrows
UPDATE burst_jobs SET state = 'abandoned', reason = 'nenhum trabalhador a tempo', finished_at = now()
WHERE id = sqlc.arg(id) AND state = 'queued' AND created_at <= now() - sqlc.arg(wait_seconds)::int * interval '1 second';

-- name: CancelBurstJob :execrows
UPDATE burst_jobs SET state = 'cancelled', reason = sqlc.arg(reason), finished_at = now()
WHERE id = sqlc.arg(id) AND state IN ('queued', 'claimed');

-- name: CancelLiveBurstJobForRun :execrows
UPDATE burst_jobs SET state = 'cancelled', reason = sqlc.arg(reason), finished_at = now()
WHERE run_id = sqlc.arg(run_id) AND state IN ('queued', 'claimed');

-- name: ExpireBurstLeases :many
UPDATE burst_jobs SET state = 'lost', reason = 'concessão expirada', finished_at = now()
WHERE state = 'claimed' AND lease_expires_at < now()
RETURNING id;

-- A cota de eventos por trabalho: a linha do trabalho fica travada enquanto o lote entra.
-- name: LockBurstJobEvents :one
SELECT events_count, events_bytes FROM burst_jobs WHERE id = $1 FOR UPDATE;

-- Um evento por linha, todos do lote na mesma transação; um seq já gravado é ignorado.
-- name: InsertBurstEvent :execrows
INSERT INTO burst_events (job_id, org_id, seq, event)
VALUES ($1, $2, $3, $4)
ON CONFLICT (job_id, seq) DO NOTHING;

-- name: AddBurstJobEvents :exec
UPDATE burst_jobs SET events_count = events_count + sqlc.arg(events)::int, events_bytes = events_bytes + sqlc.arg(bytes)::bigint
WHERE id = sqlc.arg(id);

-- Os eventos depois de after_seq, em ordem, até max_rows linhas e max_bytes bytes (a primeira
-- linha sempre vem, para o leitor nunca parar).
-- name: ListBurstEvents :many
SELECT e.seq, e.event FROM (
  SELECT seq, event, sum(length(event)) OVER (ORDER BY seq) AS acc, row_number() OVER (ORDER BY seq) AS rn
  FROM burst_events
  WHERE job_id = sqlc.arg(job_id) AND seq > sqlc.arg(after_seq)
  ORDER BY seq
  LIMIT sqlc.arg(max_rows)
) e
WHERE e.acc <= sqlc.arg(max_bytes)::bigint OR e.rn = 1
ORDER BY e.seq;

-- name: TouchBurstInstance :exec
INSERT INTO burst_instances (instance_id, instance_type, lifecycle, first_seen_at, last_seen_at)
VALUES (sqlc.arg(instance_id), sqlc.arg(instance_type), sqlc.arg(lifecycle), now(), now())
ON CONFLICT (instance_id) DO UPDATE SET
  last_seen_at = GREATEST(burst_instances.last_seen_at, EXCLUDED.last_seen_at),
  instance_type = CASE WHEN EXCLUDED.instance_type <> '' THEN EXCLUDED.instance_type ELSE burst_instances.instance_type END,
  lifecycle = CASE WHEN EXCLUDED.lifecycle <> '' THEN EXCLUDED.lifecycle ELSE burst_instances.lifecycle END;

-- O aviso de vida do trabalhador: só atualiza uma instância que já existe.
-- name: NoteBurstContact :execrows
UPDATE burst_instances SET last_contact_at = now(), agent_version = sqlc.arg(agent_version), last_error = sqlc.arg(last_error)
WHERE instance_id = sqlc.arg(instance_id);

-- name: BurstInstanceSeen :one
SELECT EXISTS (
  SELECT 1 FROM burst_instances
  WHERE instance_id = sqlc.arg(instance_id) AND last_seen_at >= now() - sqlc.arg(within_seconds)::int * interval '1 second'
)::boolean AS seen;

-- name: CountBurstJobs :one
SELECT count(*) FILTER (WHERE state = 'queued')::bigint AS queued,
       count(*) FILTER (WHERE state = 'claimed')::bigint AS busy
FROM burst_jobs WHERE state IN ('queued', 'claimed');

-- Instâncias vivas (ouvidas na janela) e as que delas estão sem trabalho.
-- name: CountLiveBurstInstances :one
SELECT count(*)::bigint AS live,
       count(*) FILTER (WHERE NOT EXISTS (
         SELECT 1 FROM burst_jobs j WHERE j.state = 'claimed' AND j.instance_id = i.instance_id
       ))::bigint AS idle
FROM burst_instances i WHERE i.last_seen_at >= now() - sqlc.arg(window_seconds)::int * interval '1 second';

-- Segundos de instância do mês: o trecho de cada instância dentro de [month_start, now()] mais o
-- boot das que nasceram nele.
-- name: BurstUsageSeconds :one
SELECT COALESCE(sum(
  GREATEST(EXTRACT(EPOCH FROM (LEAST(last_seen_at, now()) - GREATEST(first_seen_at, sqlc.arg(month_start)::timestamptz))), 0)
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
  unhealthy_until = CASE WHEN failed_claims + 1 >= sqlc.arg(threshold)::int
                         THEN now() + sqlc.arg(cooldown_seconds)::int * interval '1 second' ELSE unhealthy_until END
WHERE id = 1;

-- name: PruneBurstJobs :execrows
DELETE FROM burst_jobs
WHERE finished_at IS NOT NULL AND finished_at < now() - sqlc.arg(older_than_seconds)::int * interval '1 second'
  AND state NOT IN ('queued', 'claimed');
