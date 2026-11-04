-- 0001_initial: esquema inicial da docmatch.
--
-- Alvo: PostgreSQL 18 com pgvector. Convenções:
--   * dinheiro em bigint (centavos); tokens docmatch em bigint ("credits");
--   * chaves primárias uuid geradas com uuidv7() (nativa no PG 18; abaixo há um
--     substituto para versões anteriores);
--   * toda tabela de locatário tem row-level security ligada e forçada, com a
--     política `org_id = app_org_id() OR app_is_system()`;
--   * tabelas que crescem sem parar (credit_ledger, findings, api_usage,
--     audit_log) são particionadas por mês; ensure_month_partitions() cria os
--     meses seguintes e é chamada por um job periódico do worker;
--   * o papel da aplicação vem do GUC docmatch.app_role, definido pelo
--     cmd/migrate a partir de DM_DB_APP_ROLE (padrão docmatch_app). Ele é
--     NOBYPASSRLS e não tem UPDATE/DELETE em credit_ledger, audit_log e feedback.

-- ---------------------------------------------------------------- extensões
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS vector;

-- ------------------------------------------------------------------- uuidv7
-- Substituto para clusters sem uuidv7() nativa (PG < 18): 48 bits de tempo em
-- milissegundos seguidos de bits aleatórios, com versão 7 e variante RFC 4122.
CREATE OR REPLACE FUNCTION docmatch_uuidv7_fallback() RETURNS uuid
LANGUAGE sql VOLATILE PARALLEL SAFE AS $$
  SELECT encode(
    set_bit(
      set_bit(
        overlay(uuid_send(gen_random_uuid())
                placing substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
                FROM 1 FOR 6),
        52, 1),
      53, 1),
    'hex')::uuid;
$$;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = 'pg_catalog' AND p.proname = 'uuidv7'
  ) THEN
    EXECUTE 'CREATE FUNCTION public.uuidv7() RETURNS uuid LANGUAGE sql VOLATILE PARALLEL SAFE AS $f$ SELECT docmatch_uuidv7_fallback() $f$';
  END IF;
END $$;

-- ------------------------------------------------------- papel da aplicação
CREATE OR REPLACE FUNCTION app_org_id() RETURNS uuid
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT nullif(current_setting('app.org_id', true), '')::uuid;
$$;

CREATE OR REPLACE FUNCTION app_is_system() RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT coalesce(current_setting('app.system', true), '') = 'on';
$$;

DO $$
DECLARE
  r text := coalesce(nullif(current_setting('docmatch.app_role', true), ''), 'docmatch_app');
BEGIN
  IF r !~ '^[A-Za-z_][A-Za-z0-9_]{0,62}$' THEN
    RAISE EXCEPTION 'docmatch.app_role inválido: %', r;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
    EXECUTE format('CREATE ROLE %I NOLOGIN NOINHERIT NOBYPASSRLS', r);
  END IF;
  EXECUTE format('ALTER ROLE %I NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE', r);
  -- O usuário que migra (dono das tabelas) pode assumir o papel da aplicação.
  IF NOT EXISTS (
    SELECT 1 FROM pg_auth_members m
    JOIN pg_roles g ON g.oid = m.roleid
    WHERE g.rolname = r AND m.member = (SELECT oid FROM pg_roles WHERE rolname = current_user)
  ) THEN
    EXECUTE format('GRANT %I TO %I', r, current_user);
  END IF;
  EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', r);
  -- Guarda o nome para as funções de manutenção.
  EXECUTE format('CREATE OR REPLACE FUNCTION app_role_name() RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS %L', 'SELECT ' || quote_literal(r));
END $$;

-- ------------------------------------------------------------------ gatilhos
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END $$;

-- -------------------------------------------------------------------- planos
CREATE TABLE plans (
  code        text PRIMARY KEY,
  name        text NOT NULL,
  sort_order  int  NOT NULL DEFAULT 0,
  active      boolean NOT NULL DEFAULT true,
  -- Sobrescritas aplicadas pelo painel admin; config/pricing.yaml é a fonte padrão.
  overrides   jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
INSERT INTO plans (code, name, sort_order) VALUES
  ('starter', 'Starter', 1),
  ('pro', 'Pro', 2),
  ('business', 'Business', 3);

-- --------------------------------------------------------- organizações e usuários
CREATE TABLE orgs (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  name        text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  slug        citext NOT NULL UNIQUE,
  plan_code   text NOT NULL DEFAULT 'starter' REFERENCES plans (code),
  country     char(2),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz
);
CREATE TRIGGER orgs_updated_at BEFORE UPDATE ON orgs FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE users (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  email              citext NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
  google_sub         text,
  apple_sub          text,
  password_hash      text,
  display_name       text CHECK (display_name IS NULL OR char_length(display_name) <= 80),
  email_verified_at  timestamptz,
  last_login_at      timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  deleted_at         timestamptz
);
CREATE UNIQUE INDEX users_email_active_idx ON users (email) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX users_google_sub_idx ON users (google_sub) WHERE google_sub IS NOT NULL;
CREATE UNIQUE INDEX users_apple_sub_idx ON users (apple_sub) WHERE apple_sub IS NOT NULL;
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE memberships (
  org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  role        text NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, user_id)
);
CREATE INDEX memberships_user_idx ON memberships (user_id);

-- Sessões são do usuário (existem antes de escolher uma organização); a
-- consulta quente é por chave primária e por refresh_hash.
CREATE TABLE sessions (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  org_id        uuid REFERENCES orgs (id) ON DELETE SET NULL,
  device_hash   bytea NOT NULL,
  refresh_hash  bytea NOT NULL UNIQUE,
  family_id     uuid NOT NULL,
  ip_prefix     text,
  user_agent    text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  revoked_at    timestamptz
);
CREATE INDEX sessions_user_active_idx ON sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX sessions_family_idx ON sessions (family_id);

CREATE TABLE magic_links (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  email        citext NOT NULL,
  token_hash   bytea NOT NULL UNIQUE,
  purpose      text NOT NULL CHECK (purpose IN ('login', 'verify_email', 'reset_password')),
  ip_prefix    text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  consumed_at  timestamptz
);
CREATE INDEX magic_links_email_idx ON magic_links (email, created_at DESC);

-- ------------------------------------------------------------------ cobrança
CREATE TABLE subscriptions (
  id                      uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id                  uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  plan_code               text NOT NULL REFERENCES plans (code),
  stripe_customer_id      text,
  stripe_subscription_id  text UNIQUE,
  status                  text NOT NULL CHECK (status IN ('trialing', 'active', 'past_due', 'canceled', 'unpaid', 'incomplete', 'paused')),
  current_period_start    timestamptz,
  current_period_end      timestamptz,
  cancel_at               timestamptz,
  canceled_at             timestamptz,
  created_at              timestamptz NOT NULL DEFAULT now(),
  updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX subscriptions_org_idx ON subscriptions (org_id, created_at DESC);
CREATE INDEX subscriptions_stripe_customer_idx ON subscriptions (stripe_customer_id) WHERE stripe_customer_id IS NOT NULL;
CREATE TRIGGER subscriptions_updated_at BEFORE UPDATE ON subscriptions FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A chave primária é o id do evento Stripe: inserir duas vezes é impossível.
CREATE TABLE stripe_events (
  id            text PRIMARY KEY,
  type          text NOT NULL,
  api_version   text,
  payload       jsonb NOT NULL,
  received_at   timestamptz NOT NULL DEFAULT now(),
  processed_at  timestamptz,
  error         text
);
CREATE INDEX stripe_events_unprocessed_idx ON stripe_events (received_at) WHERE processed_at IS NULL;

-- --------------------------------------------------------------- tokens docmatch
-- ledger_seq é o contador do razão da organização: cada lançamento recebe
-- ledger_seq + 1 sob o lock da linha da conta, o que dá uma ordem total por
-- organização independente de relógio (now() é o início da transação e não
-- reflete a ordem de commit entre transações concorrentes).
CREATE TABLE credit_accounts (
  org_id      uuid PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
  balance     bigint NOT NULL DEFAULT 0,
  held        bigint NOT NULL DEFAULT 0,
  ledger_seq  bigint NOT NULL DEFAULT 0,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT credit_accounts_balance_covers_held CHECK (balance >= held AND held >= 0)
);

CREATE TABLE credit_grants (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id           uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  kind             text NOT NULL CHECK (kind IN ('plan', 'topup', 'bonus', 'refund', 'adjustment')),
  amount           bigint NOT NULL CHECK (amount > 0),
  remaining        bigint NOT NULL CHECK (remaining >= 0),
  source           text,
  idempotency_key  text,
  granted_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz,
  expired_at       timestamptz,
  CONSTRAINT credit_grants_remaining_le_amount CHECK (remaining <= amount)
);
CREATE UNIQUE INDEX credit_grants_idempotency_idx ON credit_grants (org_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX credit_grants_open_idx ON credit_grants (org_id, expires_at NULLS LAST, granted_at) WHERE expired_at IS NULL AND remaining > 0;
CREATE INDEX credit_grants_expiring_idx ON credit_grants (expires_at) WHERE expired_at IS NULL AND remaining > 0 AND expires_at IS NOT NULL;

CREATE TABLE credit_holds (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id          uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  amount          bigint NOT NULL CHECK (amount > 0),
  status          text NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'settled', 'released')),
  settled_amount  bigint CHECK (settled_amount IS NULL OR (settled_amount >= 0 AND settled_amount <= amount)),
  run_id          uuid,
  quote_id        uuid,
  reference       text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  closed_at       timestamptz
);
CREATE INDEX credit_holds_open_idx ON credit_holds (org_id, created_at) WHERE status = 'held';
CREATE INDEX credit_holds_run_idx ON credit_holds (run_id) WHERE run_id IS NOT NULL;

-- Razão: só INSERT para a aplicação. amount é o delta do saldo, held_delta o
-- delta da reserva; balance_after/held_after permitem auditar sem somar tudo.
CREATE TABLE credit_ledger (
  id             uuid NOT NULL DEFAULT uuidv7(),
  org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  seq            bigint NOT NULL CHECK (seq > 0),
  kind           text NOT NULL CHECK (kind IN ('grant', 'hold', 'settle', 'release', 'expire', 'adjust')),
  amount         bigint NOT NULL,
  held_delta     bigint NOT NULL DEFAULT 0,
  balance_after  bigint NOT NULL,
  held_after     bigint NOT NULL,
  grant_id       uuid,
  hold_id        uuid,
  run_id         uuid,
  reference      text,
  meta           jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX credit_ledger_org_seq_idx ON credit_ledger (org_id, seq DESC);
CREATE INDEX credit_ledger_org_created_idx ON credit_ledger (org_id, created_at DESC);
CREATE INDEX credit_ledger_hold_idx ON credit_ledger (hold_id) WHERE hold_id IS NOT NULL;
CREATE TABLE credit_ledger_default PARTITION OF credit_ledger DEFAULT;

CREATE TABLE quotes (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id             uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  user_id            uuid REFERENCES users (id) ON DELETE SET NULL,
  lane               text NOT NULL CHECK (lane IN ('app', 'api_batch', 'api_standard', 'api_priority')),
  estimated_credits  bigint NOT NULL CHECK (estimated_credits >= 0),
  cap_credits        bigint NOT NULL CHECK (cap_credits >= estimated_credits),
  inputs             jsonb NOT NULL DEFAULT '{}'::jsonb,
  signature          bytea NOT NULL,
  status             text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'accepted', 'expired', 'used')),
  expires_at         timestamptz NOT NULL,
  accepted_at        timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX quotes_org_created_idx ON quotes (org_id, created_at DESC);

-- -------------------------------------------------------- fluxos e execuções
CREATE TABLE workflows (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  name         text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  description  text,
  config       jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  deleted_at   timestamptz
);
CREATE INDEX workflows_org_idx ON workflows (org_id, updated_at DESC) WHERE deleted_at IS NULL;
CREATE TRIGGER workflows_updated_at BEFORE UPDATE ON workflows FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A chave de API é "dm_<id>_<segredo>": a busca quente é por id (PK) e o
-- segredo é conferido contra key_hash (HMAC com DM_APIKEY_PEPPER).
CREATE TABLE api_keys (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id              uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  label               text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 60),
  prefix              text NOT NULL,
  key_hash            bytea NOT NULL UNIQUE,
  scopes              text[] NOT NULL DEFAULT '{}' CHECK (cardinality(scopes) <= 10),
  lanes               text[] NOT NULL DEFAULT '{api_batch}',
  rate_limit_per_min  int CHECK (rate_limit_per_min IS NULL OR rate_limit_per_min > 0),
  created_by          uuid REFERENCES users (id) ON DELETE SET NULL,
  last_used_at        timestamptz,
  expires_at          timestamptz,
  revoked_at          timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_org_active_idx ON api_keys (org_id) WHERE revoked_at IS NULL;

CREATE TABLE runs (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id           uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  workflow_id      uuid REFERENCES workflows (id) ON DELETE SET NULL,
  user_id          uuid REFERENCES users (id) ON DELETE SET NULL,
  api_key_id       uuid REFERENCES api_keys (id) ON DELETE SET NULL,
  quote_id         uuid REFERENCES quotes (id) ON DELETE SET NULL,
  hold_id          uuid REFERENCES credit_holds (id) ON DELETE SET NULL,
  lane             text NOT NULL CHECK (lane IN ('app', 'api_batch', 'api_standard', 'api_priority')),
  status           text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
  credits_held     bigint NOT NULL DEFAULT 0,
  credits_settled  bigint NOT NULL DEFAULT 0,
  error_code       text,
  stats            jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at       timestamptz,
  finished_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runs_org_recent_idx ON runs (org_id, created_at DESC);
CREATE INDEX runs_active_idx ON runs (org_id, status) WHERE status IN ('queued', 'running');
CREATE INDEX runs_queue_idx ON runs (created_at) WHERE status = 'queued';
CREATE TRIGGER runs_updated_at BEFORE UPDATE ON runs FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE uploads (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id        uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  user_id       uuid REFERENCES users (id) ON DELETE SET NULL,
  run_id        uuid REFERENCES runs (id) ON DELETE SET NULL,
  file_name     text NOT NULL CHECK (octet_length(file_name) BETWEEN 1 AND 255),
  content_type  text,
  size_bytes    bigint NOT NULL CHECK (size_bytes >= 0),
  sha256        bytea,
  storage_key   text NOT NULL,
  status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'scanning', 'clean', 'infected', 'rejected', 'deleted')),
  scan_result   jsonb,
  created_at    timestamptz NOT NULL DEFAULT now(),
  scanned_at    timestamptz,
  deleted_at    timestamptz
);
CREATE INDEX uploads_org_created_idx ON uploads (org_id, created_at DESC);
CREATE INDEX uploads_run_idx ON uploads (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX uploads_pending_idx ON uploads (created_at) WHERE status IN ('pending', 'scanning');

CREATE TABLE findings (
  id              uuid NOT NULL DEFAULT uuidv7(),
  org_id          uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  run_id          uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
  kind            text NOT NULL,
  severity        text NOT NULL DEFAULT 'medium' CHECK (severity IN ('low', 'medium', 'high', 'critical')),
  amount_at_risk  bigint NOT NULL DEFAULT 0,
  currency        char(3) NOT NULL DEFAULT 'BRL',
  title           text NOT NULL,
  explanation     text,
  evidence        jsonb NOT NULL DEFAULT '[]'::jsonb,
  verified        boolean NOT NULL DEFAULT false,
  verification    jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX findings_run_amount_idx ON findings (run_id, amount_at_risk DESC);
CREATE INDEX findings_org_created_idx ON findings (org_id, created_at DESC);
CREATE TABLE findings_default PARTITION OF findings DEFAULT;

CREATE TABLE matches (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  run_id      uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
  left_ref    text NOT NULL,
  right_ref   text NOT NULL,
  rule_id     uuid,
  confidence  real NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
  amount      bigint,
  meta        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX matches_run_idx ON matches (run_id);

-- -------------------------------------------------------------------- regras
CREATE TABLE rules (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id        uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  title         text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
  body          text NOT NULL CHECK (char_length(body) <= 2000),
  compiled_sql  text,
  kind          text NOT NULL DEFAULT 'match' CHECK (kind IN ('match', 'tolerance', 'exclusion', 'transform')),
  source        text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'learned')),
  enabled       boolean NOT NULL DEFAULT true,
  created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
  use_count     bigint NOT NULL DEFAULT 0,
  last_used_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz
);
CREATE INDEX rules_title_trgm_idx ON rules USING gin (title gin_trgm_ops);
CREATE INDEX rules_org_last_used_idx ON rules (org_id, last_used_at DESC NULLS LAST) WHERE deleted_at IS NULL;
CREATE TRIGGER rules_updated_at BEFORE UPDATE ON rules FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE rule_uses (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id            uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  rule_id           uuid NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
  run_id            uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
  matched           int NOT NULL DEFAULT 0,
  false_positives   int NOT NULL DEFAULT 0,
  amount_explained  bigint NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rule_uses_rule_idx ON rule_uses (rule_id, created_at DESC);
CREATE INDEX rule_uses_run_idx ON rule_uses (run_id);

-- Feedback é só INSERT para a aplicação: a opinião dada fica registrada.
CREATE TABLE feedback (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id              uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  user_id             uuid REFERENCES users (id) ON DELETE SET NULL,
  run_id              uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
  finding_id          uuid NOT NULL,
  finding_created_at  timestamptz NOT NULL,
  verdict             text NOT NULL CHECK (verdict IN ('correct', 'incorrect', 'unsure')),
  note                text CHECK (note IS NULL OR char_length(note) <= 500),
  created_at          timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (finding_id, finding_created_at) REFERENCES findings (id, created_at) ON DELETE CASCADE
);
CREATE INDEX feedback_finding_idx ON feedback (finding_id);
CREATE INDEX feedback_org_idx ON feedback (org_id, created_at DESC);

-- ---------------------------------------------------------------- API pública
CREATE TABLE api_usage (
  id           uuid NOT NULL DEFAULT uuidv7(),
  org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  api_key_id   uuid,
  route        text NOT NULL,
  method       text NOT NULL,
  status       int NOT NULL,
  lane         text,
  credits      bigint NOT NULL DEFAULT 0,
  duration_ms  int NOT NULL DEFAULT 0,
  request_id   text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX api_usage_org_idx ON api_usage (org_id, created_at DESC);
CREATE INDEX api_usage_key_idx ON api_usage (api_key_id, created_at DESC) WHERE api_key_id IS NOT NULL;
CREATE TABLE api_usage_default PARTITION OF api_usage DEFAULT;

-- ------------------------------------------------------- personalização da UI
CREATE TABLE customizations (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  user_id     uuid REFERENCES users (id) ON DELETE SET NULL,
  request     text NOT NULL CHECK (char_length(request) BETWEEN 1 AND 2000),
  status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'planned', 'applied', 'rejected', 'failed')),
  plan        jsonb,
  result      jsonb,
  credits     bigint NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  applied_at  timestamptz
);
CREATE INDEX customizations_org_idx ON customizations (org_id, created_at DESC);

CREATE TABLE tenant_themes (
  org_id      uuid PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
  tokens      jsonb NOT NULL DEFAULT '{}'::jsonb,
  css         text,
  version     int NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------- blog
CREATE TABLE blog_topics (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  slug             citext NOT NULL UNIQUE,
  title            text NOT NULL,
  description      text,
  priority         int NOT NULL DEFAULT 0,
  last_written_at  timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE blog_posts (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  topic_id      uuid REFERENCES blog_topics (id) ON DELETE SET NULL,
  slug          citext NOT NULL UNIQUE,
  title         text NOT NULL,
  summary       text,
  body_md       text NOT NULL,
  status        text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'review', 'published', 'archived')),
  model_meta    jsonb NOT NULL DEFAULT '{}'::jsonb,
  judge_score   real,
  published_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX blog_posts_published_idx ON blog_posts (published_at DESC) WHERE status = 'published';
CREATE TRIGGER blog_posts_updated_at BEFORE UPDATE ON blog_posts FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- -------------------------------------------------------------------- preços
-- Preços por milhão de tokens em centavos de dólar; histórico por fetched_at.
CREATE TABLE model_prices (
  model                      text NOT NULL,
  fetched_at                 timestamptz NOT NULL DEFAULT now(),
  input_cents_per_mtok       bigint NOT NULL CHECK (input_cents_per_mtok >= 0),
  output_cents_per_mtok      bigint NOT NULL CHECK (output_cents_per_mtok >= 0),
  cache_read_cents_per_mtok  bigint CHECK (cache_read_cents_per_mtok IS NULL OR cache_read_cents_per_mtok >= 0),
  context_length             int,
  source                     text NOT NULL DEFAULT 'openrouter',
  meta                       jsonb NOT NULL DEFAULT '{}'::jsonb,
  PRIMARY KEY (model, fetched_at)
);

-- Multiplicador de preço por país em pontos-base (10000 = 1,00x).
CREATE TABLE geo_prices (
  country        char(2) PRIMARY KEY,
  multiplier_bp  int NOT NULL CHECK (multiplier_bp > 0),
  currency       char(3) NOT NULL DEFAULT 'USD',
  note           text,
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- ----------------------------------------------------------------- auditoria
-- Cadeia de hashes por organização (chain_id; eventos de sistema usam o uuid
-- zero): hash = sha256(canônico(linha) || prev_hash). seq é estritamente
-- crescente dentro da cadeia; internal/audit faz o append com advisory lock e
-- verifica a cadeia recomputando os hashes.
CREATE TABLE audit_log (
  id           uuid NOT NULL DEFAULT uuidv7(),
  org_id       uuid,
  chain_id     uuid NOT NULL GENERATED ALWAYS AS (coalesce(org_id, '00000000-0000-0000-0000-000000000000'::uuid)) STORED,
  seq          bigint NOT NULL CHECK (seq > 0),
  actor_type   text NOT NULL CHECK (actor_type IN ('user', 'api_key', 'system', 'admin')),
  actor_id     text NOT NULL,
  action       text NOT NULL,
  target_type  text NOT NULL,
  target_id    text,
  meta         jsonb NOT NULL DEFAULT '{}'::jsonb,
  prev_hash    bytea,
  hash         bytea NOT NULL CHECK (octet_length(hash) = 32),
  request_id   text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX audit_log_chain_seq_idx ON audit_log (chain_id, seq DESC);
CREATE INDEX audit_log_org_created_idx ON audit_log (org_id, created_at DESC);
CREATE TABLE audit_log_default PARTITION OF audit_log DEFAULT;

-- -------------------------------------------------------------------- outbox
CREATE TABLE outbox (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id        uuid,
  kind          text NOT NULL,
  payload       jsonb NOT NULL,
  available_at  timestamptz NOT NULL DEFAULT now(),
  attempts      int NOT NULL DEFAULT 0,
  last_error    text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  sent_at       timestamptz
);
CREATE INDEX outbox_unsent_idx ON outbox (available_at) WHERE sent_at IS NULL;

-- ------------------------------------------------------- biblioteca de ferramentas
CREATE TABLE tools (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  slug           citext NOT NULL UNIQUE,
  name           text NOT NULL,
  kind           text NOT NULL CHECK (kind IN ('parser', 'transformer', 'matcher', 'verifier')),
  description    text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  deprecated_at  timestamptz
);
CREATE TRIGGER tools_updated_at BEFORE UPDATE ON tools FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE tool_versions (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tool_id        uuid NOT NULL REFERENCES tools (id) ON DELETE CASCADE,
  version        int NOT NULL CHECK (version > 0),
  manifest       jsonb NOT NULL,
  accepts        jsonb NOT NULL DEFAULT '[]'::jsonb,
  behavior_hash  bytea NOT NULL,
  embedding      vector(1024),
  bundle_key     text,
  status         text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'reviewed', 'published', 'rejected', 'retired')),
  review         jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  published_at   timestamptz,
  UNIQUE (tool_id, version)
);
CREATE INDEX tool_versions_embedding_idx ON tool_versions USING hnsw (embedding vector_cosine_ops);
CREATE INDEX tool_versions_behavior_idx ON tool_versions (behavior_hash);
CREATE INDEX tool_versions_published_idx ON tool_versions (tool_id, version DESC) WHERE status = 'published';

CREATE TABLE tool_runs (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id           uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  run_id           uuid REFERENCES runs (id) ON DELETE CASCADE,
  tool_version_id  uuid NOT NULL REFERENCES tool_versions (id) ON DELETE RESTRICT,
  status           text NOT NULL CHECK (status IN ('ok', 'failed', 'timeout', 'rejected')),
  duration_ms      int NOT NULL DEFAULT 0,
  input_sha256     bytea,
  output_summary   jsonb,
  error            text,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tool_runs_run_idx ON tool_runs (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX tool_runs_version_idx ON tool_runs (tool_version_id, created_at DESC);

-- Impressões digitais de formato (estrutura, nunca conteúdo) para reencontrar
-- ferramentas já construídas.
CREATE TABLE source_fingerprints (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  fingerprint_hash  bytea NOT NULL UNIQUE,
  kind              text NOT NULL,
  structure         jsonb NOT NULL DEFAULT '{}'::jsonb,
  embedding         vector(1024),
  tool_version_id   uuid REFERENCES tool_versions (id) ON DELETE SET NULL,
  hits              bigint NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  last_seen_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX source_fingerprints_embedding_idx ON source_fingerprints USING hnsw (embedding vector_cosine_ops);

CREATE TABLE library_requests (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id          uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  run_id          uuid REFERENCES runs (id) ON DELETE SET NULL,
  fingerprint_id  uuid REFERENCES source_fingerprints (id) ON DELETE SET NULL,
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'building', 'reviewing', 'done', 'failed', 'rejected')),
  request         jsonb NOT NULL DEFAULT '{}'::jsonb,
  result          jsonb,
  credits         bigint NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX library_requests_org_idx ON library_requests (org_id, created_at DESC);
CREATE INDEX library_requests_open_idx ON library_requests (created_at) WHERE status IN ('open', 'building', 'reviewing');
CREATE TRIGGER library_requests_updated_at BEFORE UPDATE ON library_requests FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- --------------------------------------------------------------------- mídia
CREATE TABLE media_assets (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id          uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  upload_id       uuid REFERENCES uploads (id) ON DELETE SET NULL,
  kind            text NOT NULL CHECK (kind IN ('audio', 'video', 'image')),
  r2_key          text NOT NULL,
  size_bytes      bigint NOT NULL CHECK (size_bytes >= 0),
  duration_ms     bigint CHECK (duration_ms IS NULL OR duration_ms >= 0),
  sha256          bytea,
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'ready', 'failed', 'deleted')),
  transcript_key  text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  deleted_at      timestamptz
);
CREATE INDEX media_assets_org_idx ON media_assets (org_id, created_at DESC);

-- ----------------------------------------------------------- partições mensais
-- Cria as partições do mês corrente, de months_back meses para trás e de
-- months_ahead meses para a frente, para cada tabela particionada. Idempotente;
-- devolve uma linha por partição com created=true quando a criou agora.
-- SECURITY DEFINER para que o worker (papel da aplicação) possa chamá-la.
CREATE OR REPLACE FUNCTION ensure_month_partitions(months_ahead int DEFAULT 1, months_back int DEFAULT 0)
RETURNS TABLE (partition_name text, created boolean)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
  parent       text;
  m            int;
  month_utc    timestamp;
  lower_bound  timestamptz;
  upper_bound  timestamptz;
BEGIN
  IF months_ahead < 0 OR months_back < 0 OR months_ahead > 24 OR months_back > 120 THEN
    RAISE EXCEPTION 'ensure_month_partitions: intervalo inválido (% atrás, % à frente)', months_back, months_ahead;
  END IF;
  FOREACH parent IN ARRAY ARRAY['credit_ledger', 'findings', 'api_usage', 'audit_log'] LOOP
    FOR m IN -months_back..months_ahead LOOP
      month_utc := date_trunc('month', now() AT TIME ZONE 'UTC') + make_interval(months => m);
      lower_bound := month_utc AT TIME ZONE 'UTC';
      upper_bound := (month_utc + interval '1 month') AT TIME ZONE 'UTC';
      partition_name := format('%s_y%sm%s', parent, to_char(month_utc, 'YYYY'), to_char(month_utc, 'MM'));
      IF to_regclass(partition_name) IS NULL THEN
        EXECUTE format('CREATE TABLE %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',
                       partition_name, parent, lower_bound, upper_bound);
        created := true;
      ELSE
        created := false;
      END IF;
      RETURN NEXT;
    END LOOP;
  END LOOP;
END $$;

SELECT * FROM ensure_month_partitions(1, 0);

-- ------------------------------------------------------- row-level security
-- Toda tabela de locatário: políticas para qualquer papel (o dono também está
-- sujeito por FORCE), de modo que um esquecimento de WHERE org_id nunca
-- atravessa locatários. app.system = 'on' é o modo de manutenção dos workers.
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'memberships', 'subscriptions', 'credit_accounts', 'credit_grants', 'credit_holds', 'credit_ledger',
    'quotes', 'workflows', 'api_keys', 'runs', 'uploads', 'findings', 'matches', 'rules', 'rule_uses',
    'feedback', 'api_usage', 'customizations', 'tenant_themes', 'audit_log', 'tool_runs',
    'library_requests', 'media_assets'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I FOR ALL TO PUBLIC USING (org_id = app_org_id() OR app_is_system()) WITH CHECK (org_id = app_org_id() OR app_is_system())', t);
  END LOOP;
END $$;

ALTER TABLE orgs ENABLE ROW LEVEL SECURITY;
ALTER TABLE orgs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON orgs FOR ALL TO PUBLIC
  USING (id = app_org_id() OR app_is_system())
  WITH CHECK (id = app_org_id() OR app_is_system());

-- --------------------------------------------------------------- privilégios
-- O papel da aplicação acessa só as tabelas-mãe (partições são alcançadas
-- através delas); credit_ledger, audit_log e feedback são só INSERT.
DO $$
DECLARE
  r text := app_role_name();
  t text;
BEGIN
  FOR t IN
    SELECT c.relname FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
      AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid)
  LOOP
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO %I', t, r);
  END LOOP;
  EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON credit_ledger, audit_log, feedback FROM %I', r);
  EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I', r);
  EXECUTE format('GRANT EXECUTE ON FUNCTION ensure_month_partitions(int, int) TO %I', r);
  EXECUTE format('GRANT EXECUTE ON FUNCTION app_org_id(), app_is_system(), app_role_name(), docmatch_uuidv7_fallback() TO %I', r);
END $$;
