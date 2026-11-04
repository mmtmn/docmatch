-- Chaves de dados por locatário (internal/crypto + internal/account). Dentro
-- de WithOrgTx; o worker de exclusão usa modo sistema.

-- name: AuthInsertTenantKey :one
INSERT INTO tenant_keys (org_id, wrapped_key, kek_version)
VALUES ($1, $2, $3)
RETURNING *;

-- name: AuthTenantKey :one
SELECT * FROM tenant_keys WHERE org_id = $1;

-- Destruir a chave é o primeiro passo da exclusão LGPD (crypto-shredding).
-- name: AuthDeleteTenantKey :execrows
DELETE FROM tenant_keys WHERE org_id = $1;
