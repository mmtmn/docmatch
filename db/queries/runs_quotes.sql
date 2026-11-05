-- Orçamentos assinados e tabela de preços de modelos usada pelo estimador.

-- name: InsertQuote :one
INSERT INTO quotes (id, org_id, user_id, lane, estimated_credits, cap_credits, inputs, signature, fingerprint, quote, run_id, kind, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetQuoteByID :one
SELECT * FROM quotes WHERE org_id = $1 AND id = $2;

-- name: SetQuoteStatus :one
UPDATE quotes
SET status = $3,
    run_id = coalesce(sqlc.narg(run_id)::uuid, run_id),
    accepted_at = CASE WHEN $3 IN ('accepted', 'used') THEN coalesce(accepted_at, now()) ELSE accepted_at END
WHERE org_id = $1 AND id = $2
RETURNING *;

-- Preço mais recente de cada modelo (histórico por fetched_at).
-- name: ListLatestModelPrices :many
SELECT DISTINCT ON (model) * FROM model_prices ORDER BY model, fetched_at DESC;

-- name: CountModelPrices :one
SELECT count(*) FROM model_prices;

-- name: InsertModelPrice :exec
INSERT INTO model_prices (model, input_cents_per_mtok, output_cents_per_mtok, cache_read_cents_per_mtok, context_length, source)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (model, fetched_at) DO NOTHING;

-- Execuções concluídas recentes (modo sistema): razão gasto/estimado para o
-- teto no percentil 99.
-- name: ListRecentRunOverruns :many
SELECT estimated_credits, credits_spent FROM runs
WHERE status = 'completed' AND finished_at > $1 AND estimated_credits > 0
ORDER BY finished_at DESC
LIMIT $2;
