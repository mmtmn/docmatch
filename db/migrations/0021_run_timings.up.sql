-- 0021_run_timings: a duração medida de cada execução terminada, de onde o
-- estimador aprende o tempo esperado dos orçamentos (config/throughput.yaml),
-- e uma chave de deduplicação no outbox para e-mails que só podem sair uma
-- vez (o aviso de fim de execução longa).

-- --------------------------------------------------------------- durações
-- units: quantidade de entrada por tipo ({"table": 1.5, "audio": 42, ...});
-- active_seconds: tempo com o harness trabalhando, somado entre retomadas
-- (sem a espera na fila nem a pausa de aprovação).
CREATE TABLE run_timings (
  run_id            uuid PRIMARY KEY REFERENCES runs (id) ON DELETE CASCADE,
  org_id            uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  status            text NOT NULL CHECK (status IN ('completed', 'failed')),
  units             jsonb NOT NULL DEFAULT '{}'::jsonb,
  active_seconds    double precision NOT NULL CHECK (active_seconds >= 0),
  expected_seconds  bigint NOT NULL DEFAULT 0 CHECK (expected_seconds >= 0),
  finished_at       timestamptz NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now()
);
-- O estimador lê as concluídas mais recentes de todas as contas (modo sistema).
CREATE INDEX run_timings_recent_idx ON run_timings (finished_at DESC) WHERE status = 'completed';
CREATE INDEX run_timings_org_idx ON run_timings (org_id);

ALTER TABLE run_timings ENABLE ROW LEVEL SECURITY;
ALTER TABLE run_timings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON run_timings FOR ALL TO PUBLIC
  USING (org_id = app_org_id() OR app_is_system())
  WITH CHECK (org_id = app_org_id() OR app_is_system());

-- ------------------------------------------------------------------ outbox
-- Uma linha por chave: enfileirar de novo a mesma chave não faz nada.
ALTER TABLE outbox ADD COLUMN dedupe_key text CHECK (dedupe_key IS NULL OR char_length(dedupe_key) BETWEEN 1 AND 200);
CREATE UNIQUE INDEX outbox_dedupe_idx ON outbox (dedupe_key) WHERE dedupe_key IS NOT NULL;

-- --------------------------------------------------------------- privilégios
DO $$
BEGIN
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON run_timings TO %I', app_role_name());
END $$;
