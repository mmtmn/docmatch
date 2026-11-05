-- Desafios de login por e-mail (magic_links): um token de link (token_hash,
-- SHA-256) e um código de 6 dígitos (code_hash, HMAC) por pedido; uso único,
-- 15 minutos e no máximo cinco tentativas.

-- name: AuthCreateChallenge :one
INSERT INTO magic_links (id, email, token_hash, code_hash, purpose, ip_prefix, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: AuthChallengeByID :one
SELECT * FROM magic_links WHERE id = $1;

-- Reserva uma tentativa antes de comparar o código: só incrementa enquanto
-- attempts < $2, então tentativas em paralelo nunca passam do teto.
-- name: AuthClaimChallengeAttempt :one
UPDATE magic_links SET attempts = attempts + 1
WHERE id = $1 AND attempts < sqlc.arg(max_attempts)::int AND consumed_at IS NULL
RETURNING attempts;

-- Consome atomicamente: só a primeira chamada devolve a linha.
-- name: AuthConsumeChallenge :one
UPDATE magic_links SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING *;

-- name: AuthDeleteStaleChallenges :execrows
DELETE FROM magic_links WHERE expires_at < $1;
