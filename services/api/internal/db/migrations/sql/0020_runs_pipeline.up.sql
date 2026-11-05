-- 0020_runs_pipeline: uploads, orçamentos, execuções com eventos, achados com
-- feedback, regras versionadas e chaves de idempotência (contrato openapi.yaml).
--
-- Numeração por área (0020 = execuções): o migrador aplica toda versão ainda não
-- registrada, então lacunas entre áreas são esperadas.

-- ----------------------------------------------------------------- uploads
ALTER TABLE uploads
  ADD COLUMN kind              text NOT NULL DEFAULT 'document' CHECK (kind IN ('document', 'media')),
  ADD COLUMN store             text NOT NULL DEFAULT 's3' CHECK (store IN ('s3', 'r2', 'local')),
  ADD COLUMN role              text CHECK (role IS NULL OR role ~ '^[a-z][a-z0-9_]{0,39}$'),
  ADD COLUMN detected_mime     text,
  ADD COLUMN rejected_reason   text CHECK (rejected_reason IS NULL OR rejected_reason IN ('type', 'size', 'malware', 'bomb', 'playlist', 'name', 'checksum')),
  ADD COLUMN duration_ms       bigint CHECK (duration_ms IS NULL OR duration_ms >= 0),
  ADD COLUMN ticket_expires_at timestamptz,
  ADD COLUMN completed_at      timestamptz,
  ADD COLUMN expires_at        timestamptz;

-- ------------------------------------------------------------------ quotes
ALTER TABLE quotes
  ADD COLUMN fingerprint text,
  ADD COLUMN quote       jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN run_id      uuid,
  ADD COLUMN kind        text NOT NULL DEFAULT 'initial' CHECK (kind IN ('initial', 'additional'));
CREATE INDEX quotes_run_idx ON quotes (run_id) WHERE run_id IS NOT NULL;

-- -------------------------------------------------------------------- runs
-- Estados do contrato: queued, running, needs_approval, completed, failed, cancelled.
ALTER TABLE runs DROP CONSTRAINT runs_status_check;
-- Existing rows move to the new vocabulary. RLS is forced on runs, so the
-- update runs in maintenance mode (transaction-local) or it would touch nothing.
SELECT set_config('app.system', 'on', true);
UPDATE runs SET status = CASE status WHEN 'succeeded' THEN 'completed' WHEN 'canceled' THEN 'cancelled' END
WHERE status IN ('succeeded', 'canceled');
ALTER TABLE runs ADD CONSTRAINT runs_status_check
  CHECK (status IN ('queued', 'running', 'needs_approval', 'completed', 'failed', 'cancelled'));
ALTER TABLE runs
  ADD COLUMN estimated_credits bigint NOT NULL DEFAULT 0,
  ADD COLUMN credits_spent     bigint NOT NULL DEFAULT 0,
  ADD COLUMN credits_refunded  bigint NOT NULL DEFAULT 0,
  ADD COLUMN last_seq          bigint NOT NULL DEFAULT 0,
  ADD COLUMN inputs            jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN progress          jsonb,
  ADD COLUMN summary           jsonb,
  ADD COLUMN error             jsonb,
  ADD COLUMN pending_quote_id  uuid REFERENCES quotes (id) ON DELETE SET NULL,
  ADD COLUMN cancel_requested  boolean NOT NULL DEFAULT false,
  ADD COLUMN expires_at        timestamptz;
CREATE INDEX runs_workflow_idx ON runs (org_id, workflow_id) WHERE workflow_id IS NOT NULL;

-- Eventos da execução (fluxo SSE): seq é a sequência do harness; o gatilho
-- avisa as instâncias da API por NOTIFY. Particionado por mês (90 dias de
-- retenção, poda pelo worker).
CREATE TABLE run_events (
  id          uuid NOT NULL DEFAULT uuidv7(),
  org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  run_id      uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
  seq         bigint NOT NULL CHECK (seq > 0),
  event       text NOT NULL CHECK (event IN ('stage_started', 'progress', 'token_usage', 'finding', 'match', 'needs_approval', 'completed', 'failed')),
  data        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, seq, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX run_events_org_run_idx ON run_events (org_id, run_id, seq);
CREATE TABLE run_events_default PARTITION OF run_events DEFAULT;

CREATE OR REPLACE FUNCTION notify_run_event() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('run_events', NEW.run_id::text || ':' || NEW.seq::text);
  RETURN NEW;
END $$;
CREATE TRIGGER run_events_notify AFTER INSERT ON run_events
  FOR EACH ROW EXECUTE FUNCTION notify_run_event();

-- run_events entra na manutenção mensal de partições.
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
  FOREACH parent IN ARRAY ARRAY['credit_ledger', 'findings', 'api_usage', 'audit_log', 'run_events'] LOOP
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

-- ------------------------------------------------------------ achados e casamentos
ALTER TABLE findings DROP CONSTRAINT findings_severity_check;
ALTER TABLE findings ADD CONSTRAINT findings_severity_check
  CHECK (severity IN ('info', 'low', 'medium', 'high', 'critical'));
ALTER TABLE findings
  ADD COLUMN code        text NOT NULL DEFAULT 'finding',
  ADD COLUMN confidence  real NOT NULL DEFAULT 0 CHECK (confidence >= 0 AND confidence <= 1),
  ADD COLUMN status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'confirmed', 'dismissed', 'resolved')),
  ADD COLUMN rule_id     uuid,
  ADD COLUMN match_id    uuid,
  ADD COLUMN source_ids  jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN details     jsonb,
  ADD COLUMN feedback    jsonb,
  ADD COLUMN updated_at  timestamptz NOT NULL DEFAULT now();
CREATE INDEX findings_run_severity_idx ON findings (run_id, severity, amount_at_risk DESC);

ALTER TABLE matches
  ADD COLUMN kind            text NOT NULL DEFAULT 'exact' CHECK (kind IN ('exact', 'tolerance', 'sum', 'fuzzy', 'rule', 'model')),
  ADD COLUMN left_evidence   jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN right_evidence  jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN currency        char(3) NOT NULL DEFAULT 'BRL',
  ADD COLUMN explanation     text;

-- Feedback com os estados do contrato (confirmed, dismissed, resolved) e o motivo.
ALTER TABLE feedback DROP CONSTRAINT feedback_verdict_check;
ALTER TABLE feedback ADD CONSTRAINT feedback_verdict_check
  CHECK (verdict IN ('correct', 'incorrect', 'unsure', 'confirmed', 'dismissed', 'resolved'));
ALTER TABLE feedback
  ADD COLUMN reason text CHECK (reason IS NULL OR reason IN ('false_positive', 'already_known', 'wrong_amount', 'duplicate', 'other'));

-- -------------------------------------------------------------------- regras
ALTER TABLE rules
  ADD COLUMN version      int NOT NULL DEFAULT 1 CHECK (version > 0),
  ADD COLUMN status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'candidate', 'verified', 'trusted', 'retired')),
  ADD COLUMN scope        text NOT NULL DEFAULT 'tenant' CHECK (scope IN ('tenant', 'global')),
  ADD COLUMN applies_to   jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN compiled     jsonb,
  ADD COLUMN stats        jsonb NOT NULL DEFAULT '{"runs_evaluated": 0, "true_positives": 0, "false_positives": 0, "explained_amount": 0, "currency": "BRL"}'::jsonb,
  ADD COLUMN provenance   jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN run_id       uuid,
  ADD COLUMN promoted_at  timestamptz;
CREATE INDEX rules_body_trgm_idx ON rules USING gin (body gin_trgm_ops);
CREATE INDEX rules_org_status_idx ON rules (org_id, status) WHERE deleted_at IS NULL;

CREATE TABLE rule_versions (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  rule_id     uuid NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
  version     int NOT NULL CHECK (version > 0),
  title       text NOT NULL,
  body        text NOT NULL,
  compiled    jsonb,
  status      text NOT NULL CHECK (status IN ('draft', 'candidate', 'verified', 'trusted', 'retired')),
  created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rule_id, version)
);
CREATE INDEX rule_versions_rule_idx ON rule_versions (rule_id, version DESC);

-- ------------------------------------------------------- chaves de idempotência
-- Guardadas por 24 h; a mesma chave com o mesmo hash devolve a resposta
-- original, com outro hash devolve idempotency_key_reused.
CREATE TABLE idempotency_keys (
  org_id           uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  key              text NOT NULL CHECK (char_length(key) BETWEEN 1 AND 128),
  request_hash     bytea NOT NULL,
  status           text NOT NULL DEFAULT 'in_progress' CHECK (status IN ('in_progress', 'done')),
  response_status  int,
  response_body    jsonb,
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  PRIMARY KEY (org_id, key)
);
CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys (expires_at);

-- ------------------------------------------------------- row-level security
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['run_events', 'rule_versions', 'idempotency_keys'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I FOR ALL TO PUBLIC USING (org_id = app_org_id() OR app_is_system()) WITH CHECK (org_id = app_org_id() OR app_is_system())', t);
  END LOOP;
END $$;

-- --------------------------------------------------------------- privilégios
DO $$
DECLARE
  r text := app_role_name();
BEGIN
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON run_events, rule_versions, idempotency_keys TO %I', r);
  -- Eventos são só INSERT para a aplicação (a poda é por partição, pelo dono).
  EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON run_events FROM %I', r);
END $$;
