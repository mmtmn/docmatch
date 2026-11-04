-- Tokens docmatch. Toda consulta roda dentro de WithOrgTx (app.org_id);
-- internal/credits implementa as invariantes em cima destas primitivas.

-- name: GetCreditAccount :one
SELECT * FROM credit_accounts WHERE org_id = $1;

-- name: GetCreditAccountForUpdate :one
SELECT * FROM credit_accounts WHERE org_id = $1 FOR UPDATE;

-- name: EnsureCreditAccount :one
INSERT INTO credit_accounts (org_id) VALUES ($1)
ON CONFLICT (org_id) DO UPDATE SET org_id = EXCLUDED.org_id
RETURNING *;

-- name: UpdateCreditAccount :one
UPDATE credit_accounts SET balance = $2, held = $3, updated_at = now()
WHERE org_id = $1
RETURNING *;

-- name: InsertCreditGrant :one
INSERT INTO credit_grants (org_id, kind, amount, remaining, source, idempotency_key, expires_at)
VALUES ($1, $2, $3, $3, $4, $5, $6)
RETURNING *;

-- name: GetCreditGrantByIdempotencyKey :one
SELECT * FROM credit_grants WHERE org_id = $1 AND idempotency_key = $2;

-- Concessões abertas na ordem de consumo (vence primeiro, depois a mais antiga).
-- name: ListOpenCreditGrantsForUpdate :many
SELECT * FROM credit_grants
WHERE org_id = $1 AND expired_at IS NULL AND remaining > 0
ORDER BY expires_at ASC NULLS LAST, granted_at ASC
FOR UPDATE;

-- name: ListExpiredOpenCreditGrantsForUpdate :many
SELECT * FROM credit_grants
WHERE org_id = $1 AND expired_at IS NULL AND remaining > 0 AND expires_at IS NOT NULL AND expires_at <= $2
ORDER BY expires_at ASC, granted_at ASC
FOR UPDATE;

-- name: UpdateCreditGrantRemaining :exec
UPDATE credit_grants SET remaining = $2 WHERE id = $1;

-- name: MarkCreditGrantExpired :exec
UPDATE credit_grants SET remaining = $2, expired_at = $3 WHERE id = $1;

-- Organizações com concessões vencidas (modo sistema; job periódico).
-- name: ListOrgsWithExpiredGrants :many
SELECT DISTINCT org_id FROM credit_grants
WHERE expired_at IS NULL AND remaining > 0 AND expires_at IS NOT NULL AND expires_at <= $1;

-- name: InsertCreditHold :one
INSERT INTO credit_holds (org_id, amount, run_id, quote_id, reference)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetCreditHold :one
SELECT * FROM credit_holds WHERE org_id = $1 AND id = $2;

-- name: GetCreditHoldForUpdate :one
SELECT * FROM credit_holds WHERE org_id = $1 AND id = $2 FOR UPDATE;

-- name: CloseCreditHold :one
UPDATE credit_holds SET status = $3, settled_amount = $4, closed_at = now()
WHERE org_id = $1 AND id = $2 AND status = 'held'
RETURNING *;

-- name: ListOpenCreditHolds :many
SELECT * FROM credit_holds WHERE org_id = $1 AND status = 'held' ORDER BY created_at;

-- O lançamento recebe o próximo número de sequência da conta na mesma
-- instrução; o chamador já segura o lock da linha da conta.
-- name: AppendCreditLedger :one
WITH bump AS (
  UPDATE credit_accounts SET ledger_seq = ledger_seq + 1 WHERE org_id = $1 RETURNING ledger_seq
)
INSERT INTO credit_ledger (org_id, seq, kind, amount, held_delta, balance_after, held_after, grant_id, hold_id, run_id, reference, meta)
SELECT $1, bump.ledger_seq, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11 FROM bump
RETURNING *;

-- name: ListCreditLedger :many
SELECT * FROM credit_ledger WHERE org_id = $1 ORDER BY seq DESC LIMIT $2;

-- name: ListCreditLedgerBefore :many
SELECT * FROM credit_ledger WHERE org_id = $1 AND seq < $2 ORDER BY seq DESC LIMIT $3;

-- name: SumCreditLedger :one
SELECT coalesce(sum(amount), 0)::bigint AS balance, coalesce(sum(held_delta), 0)::bigint AS held
FROM credit_ledger WHERE org_id = $1;
