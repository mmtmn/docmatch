-- Achados de uma execução, do maior valor em risco para o menor.

-- name: InsertFinding :one
INSERT INTO findings (org_id, run_id, kind, severity, amount_at_risk, currency, title, explanation, evidence, verified, verification)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: ListFindingsByRun :many
SELECT * FROM findings
WHERE org_id = $1 AND run_id = $2
ORDER BY amount_at_risk DESC, created_at DESC
LIMIT $3 OFFSET $4;

-- name: CountFindingsByRun :one
SELECT count(*) FROM findings WHERE org_id = $1 AND run_id = $2;

-- name: SumAmountAtRiskByRun :one
SELECT coalesce(sum(amount_at_risk), 0)::bigint AS amount_at_risk
FROM findings WHERE org_id = $1 AND run_id = $2;

-- name: InsertMatch :one
INSERT INTO matches (org_id, run_id, left_ref, right_ref, rule_id, confidence, amount, meta)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListMatchesByRun :many
SELECT * FROM matches WHERE org_id = $1 AND run_id = $2 ORDER BY created_at LIMIT $3 OFFSET $4;

-- name: InsertFeedback :one
INSERT INTO feedback (org_id, user_id, run_id, finding_id, finding_created_at, verdict, note)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListFeedbackByFinding :many
SELECT * FROM feedback WHERE org_id = $1 AND finding_id = $2 ORDER BY created_at;
