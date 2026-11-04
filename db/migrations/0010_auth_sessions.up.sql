-- 0010_auth_sessions: sessões com janela de acesso curta, desafios de login por
-- e-mail com código digitado, preferências e exclusão LGPD do usuário, chaves
-- de dados por locatário (crypto-shredding) e exportações de dados pessoais.

-- ------------------------------------------------------------------ sessões
-- O cookie dm_session carrega "id.segredo": access_hash é o SHA-256 do segredo
-- de acesso (10 min) e refresh_hash o do refresh (30 dias deslizantes, cookie
-- dm_refresh). A rotação acontece no lugar: apresentar um refresh antigo para
-- um id conhecido revoga a família inteira (family_id).
ALTER TABLE sessions
  ADD COLUMN access_hash        bytea,
  ADD COLUMN access_expires_at  timestamptz,
  ADD COLUMN rotated_at         timestamptz,
  ADD COLUMN rotations          int NOT NULL DEFAULT 0;

-- --------------------------------------------------------- desafios por e-mail
-- token_hash continua sendo o SHA-256 do token do link mágico (32 bytes);
-- code_hash é o HMAC do código de 6 dígitos com chave derivada de
-- DM_SESSION_SECRET (um vazamento do banco não revela os códigos). attempts
-- limita as tentativas por desafio.
ALTER TABLE magic_links
  ADD COLUMN code_hash  bytea,
  ADD COLUMN attempts   int NOT NULL DEFAULT 0;

-- ---------------------------------------------------------------- usuários
ALTER TABLE users
  ADD COLUMN notifications           jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN suspended_at            timestamptz,
  ADD COLUMN deletion_requested_at   timestamptz,
  ADD COLUMN deletion_scheduled_for  timestamptz;
CREATE INDEX users_deletion_due_idx ON users (deletion_scheduled_for) WHERE deletion_scheduled_for IS NOT NULL;

-- ------------------------------------------------------- chaves por locatário
-- Uma chave de dados aleatória por organização, embrulhada (AES-256-GCM) com a
-- KEK derivada de DM_SESSION_SECRET por HKDF. Apagar a linha destrói a chave:
-- tudo que foi cifrado com ela vira ruído (exclusão LGPD).
CREATE TABLE tenant_keys (
  org_id       uuid PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
  wrapped_key  bytea NOT NULL,
  kek_version  int NOT NULL DEFAULT 1,
  created_at   timestamptz NOT NULL DEFAULT now(),
  rotated_at   timestamptz
);
ALTER TABLE tenant_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_keys FOR ALL TO PUBLIC
  USING (org_id = app_org_id() OR app_is_system())
  WITH CHECK (org_id = app_org_id() OR app_is_system());

-- ------------------------------------------------------ exportações (LGPD)
-- Pedidas em GET /me/export, geradas pelo worker e baixadas por link assinado.
-- A tabela é do usuário (como sessions) e não tem RLS; o arquivo expira em
-- 24 h e o worker apaga o conteúdo.
CREATE TABLE account_exports (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  org_id        uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'failed', 'expired')),
  requested_at  timestamptz NOT NULL DEFAULT now(),
  ready_at      timestamptz,
  expires_at    timestamptz,
  archive       bytea,
  size_bytes    bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
  error         text
);
CREATE INDEX account_exports_user_idx ON account_exports (user_id, requested_at DESC);
CREATE INDEX account_exports_expiring_idx ON account_exports (expires_at) WHERE status = 'ready';

-- --------------------------------------------------------------- privilégios
DO $$
BEGIN
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_keys, account_exports TO %I', app_role_name());
END $$;
