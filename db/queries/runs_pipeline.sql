-- Execuções, eventos (SSE), achados, casamentos e feedback.

-- name: InsertRun :one
INSERT INTO runs (id, org_id, workflow_id, user_id, api_key_id, quote_id, hold_id, lane, status, estimated_credits, credits_held, inputs, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'queued', $9, $10, $11, $12)
RETURNING *;

-- name: GetRunSystem :one
SELECT * FROM runs WHERE id = $1;

-- name: ListRunsPage :many
SELECT * FROM runs
WHERE org_id = $1
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(workflow_id)::uuid IS NULL OR workflow_id = sqlc.narg(workflow_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: CountRunsByWorkflow :one
SELECT count(*) FROM runs WHERE org_id = $1 AND workflow_id = $2;

-- Grava todo o estado mutável de uma execução (o serviço carrega, altera e salva).
-- name: SaveRunState :one
UPDATE runs
SET status = $3, progress = $4, summary = $5, error = $6, error_code = $7,
    credits_held = $8, credits_spent = $9, credits_settled = $10, credits_refunded = $11,
    last_seq = $12, pending_quote_id = $13, hold_id = $14, cancel_requested = $15,
    started_at = $16, finished_at = $17, stats = $18
WHERE org_id = $1 AND id = $2
RETURNING *;

-- name: InsertRunEvent :one
INSERT INTO run_events (org_id, run_id, seq, event, data)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListRunEventsAfter :many
SELECT * FROM run_events
WHERE org_id = $1 AND run_id = $2 AND seq > $3
ORDER BY seq
LIMIT $4;

-- name: InsertFindingFull :one
INSERT INTO findings (id, org_id, run_id, code, kind, severity, amount_at_risk, currency, title, explanation, confidence, evidence, verified, status, rule_id, match_id, source_ids, details, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
RETURNING *;

-- name: GetFindingByID :one
SELECT * FROM findings WHERE org_id = $1 AND run_id = $2 AND id = $3;

-- Ordem do contrato: severidade, depois valor em risco.
-- name: ListFindingsPage :many
SELECT * FROM findings
WHERE org_id = $1 AND run_id = $2
  AND (cardinality(sqlc.arg(severities)::text[]) = 0 OR severity = ANY(sqlc.arg(severities)::text[]))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
         amount_at_risk DESC, created_at, id
LIMIT $3 OFFSET $4;

-- name: ListAllFindingsByRun :many
SELECT * FROM findings
WHERE org_id = $1 AND run_id = $2
ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
         amount_at_risk DESC, created_at, id;

-- name: ListFindingsWithFeedback :many
SELECT * FROM findings
WHERE org_id = $1 AND run_id = $2 AND status IN ('confirmed', 'dismissed', 'resolved')
ORDER BY created_at, id;

-- name: UpdateFindingFeedback :one
UPDATE findings SET status = $4, feedback = $5, updated_at = now()
WHERE org_id = $1 AND run_id = $2 AND id = $3
RETURNING *;

-- name: InsertMatchFull :one
INSERT INTO matches (id, org_id, run_id, kind, left_ref, right_ref, left_evidence, right_evidence, rule_id, confidence, amount, currency, explanation, meta, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: ListAllMatchesByRun :many
SELECT * FROM matches WHERE org_id = $1 AND run_id = $2 ORDER BY created_at, id;

-- name: InsertFindingFeedback :one
INSERT INTO feedback (org_id, user_id, run_id, finding_id, finding_created_at, verdict, reason, note)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: LatestFeedbackForFinding :one
SELECT * FROM feedback WHERE org_id = $1 AND finding_id = $2 ORDER BY created_at DESC, id DESC LIMIT 1;
