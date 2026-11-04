-- Resumo de créditos para GET /me (internal/account): parcela do plano e
-- parcela avulsa entre as concessões abertas, e a próxima expiração. Dentro
-- de WithOrgTx; não trava linhas.

-- name: AuthSumOpenGrantsByKind :many
SELECT kind, sum(remaining)::bigint AS remaining, min(expires_at) AS next_expiry
FROM credit_grants
WHERE org_id = $1 AND expired_at IS NULL AND remaining > 0
GROUP BY kind;
