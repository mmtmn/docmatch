-- 0022_burst_workers: o pool de trabalhadores sob demanda (docs/escala.md). Uma execução pesada é
-- entregue a uma instância Spot por meio destas tabelas: o worker principal enfileira o trabalho
-- (burst_jobs), a API entrega o manifesto assinado ao trabalhador do pool e grava os eventos que ele
-- devolve (burst_events), que o worker principal aplica à execução como faria com o harness local.
-- burst_instances mede as horas de instância do mês (o teto de DM_BURST_MONTHLY_HOURS) e burst_pool
-- guarda o disjuntor: subidas seguidas sem trabalhador põem o pool em espera.

-- ------------------------------------------------------------------ trabalhos
-- request: o pb.RunRequest (protobuf) montado pelo worker principal; token_hash: SHA-256 do token
-- do trabalho (o token em si só existe no manifesto); deadline_seconds conta a partir da entrega;
-- events_count e events_bytes somam os eventos gravados (a cota por trabalho). Todo instante de
-- concessão e de fim vem do relógio do banco (now()), nunca do de quem chama.
CREATE TABLE burst_jobs (
  id                uuid PRIMARY KEY,
  org_id            uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  run_id            uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
  state             text NOT NULL DEFAULT 'queued'
                    CHECK (state IN ('queued', 'claimed', 'done', 'interrupted', 'failed', 'lost', 'abandoned', 'cancelled')),
  request           bytea NOT NULL,
  deadline_seconds  bigint NOT NULL CHECK (deadline_seconds > 0),
  instance_id       text CHECK (instance_id IS NULL OR char_length(instance_id) BETWEEN 1 AND 64),
  token_hash        bytea CHECK (token_hash IS NULL OR length(token_hash) = 32),
  claimed_at        timestamptz,
  lease_expires_at  timestamptz,
  finished_at       timestamptz,
  reason            text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 600),
  events_count      integer NOT NULL DEFAULT 0 CHECK (events_count >= 0),
  events_bytes      bigint NOT NULL DEFAULT 0 CHECK (events_bytes >= 0),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CHECK (state <> 'claimed' OR (instance_id IS NOT NULL AND token_hash IS NOT NULL AND lease_expires_at IS NOT NULL))
);
-- No máximo um trabalho vivo por execução: um worker principal que reinicia se reconecta a ele.
CREATE UNIQUE INDEX burst_jobs_live_run_idx ON burst_jobs (run_id) WHERE state IN ('queued', 'claimed');
CREATE INDEX burst_jobs_queued_idx ON burst_jobs (created_at) WHERE state = 'queued';
CREATE INDEX burst_jobs_claimed_idx ON burst_jobs (lease_expires_at) WHERE state = 'claimed';
CREATE INDEX burst_jobs_finished_idx ON burst_jobs (finished_at) WHERE finished_at IS NOT NULL;
CREATE INDEX burst_jobs_org_idx ON burst_jobs (org_id);
CREATE INDEX burst_jobs_run_recent_idx ON burst_jobs (run_id, created_at DESC);
CREATE TRIGGER burst_jobs_updated_at BEFORE UPDATE ON burst_jobs FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ------------------------------------------------------------------- eventos
-- event: o pb.RunEvent (protobuf) como o harness do pool emitiu; seq é o do harness.
CREATE TABLE burst_events (
  job_id      uuid NOT NULL REFERENCES burst_jobs (id) ON DELETE CASCADE,
  org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
  seq         bigint NOT NULL CHECK (seq > 0),
  event       bytea NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (job_id, seq)
);
CREATE INDEX burst_events_org_idx ON burst_events (org_id);

-- ----------------------------------------------------------------- instâncias
-- Com o grupo de Auto Scaling, só o escalonador grava aqui (as instâncias que a AWS lista), a cada
-- ajuste: as horas do mês são a soma de last_seen_at - first_seen_at dentro do mês. Sem grupo
-- (trabalhadores à mão), um trabalho entregue ou uma chamada sobre ele conta, mais o boot de cada
-- instância nova. last_contact_at, agent_version e last_error vêm do aviso de vida do trabalhador
-- (o journal de uma instância Spot some com ela; o último erro fica aqui).
CREATE TABLE burst_instances (
  instance_id      text PRIMARY KEY CHECK (char_length(instance_id) BETWEEN 1 AND 64),
  instance_type    text NOT NULL DEFAULT '' CHECK (char_length(instance_type) <= 64),
  lifecycle        text NOT NULL DEFAULT '' CHECK (char_length(lifecycle) <= 32),
  first_seen_at    timestamptz NOT NULL,
  last_seen_at     timestamptz NOT NULL,
  last_contact_at  timestamptz,
  agent_version    text NOT NULL DEFAULT '' CHECK (char_length(agent_version) <= 64),
  last_error       text NOT NULL DEFAULT '' CHECK (char_length(last_error) <= 600),
  CHECK (last_seen_at >= first_seen_at)
);
CREATE INDEX burst_instances_last_seen_idx ON burst_instances (last_seen_at);

-- ------------------------------------------------------------------ disjuntor
CREATE TABLE burst_pool (
  id               smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  failed_claims    integer NOT NULL DEFAULT 0 CHECK (failed_claims >= 0),
  unhealthy_until  timestamptz,
  updated_at       timestamptz NOT NULL DEFAULT now()
);
INSERT INTO burst_pool (id) VALUES (1);
CREATE TRIGGER burst_pool_updated_at BEFORE UPDATE ON burst_pool FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- -------------------------------------------------------------- isolamento
-- Tudo aqui é lido e escrito em modo sistema (o pool atende todas as contas); trabalhos e eventos
-- levam org_id e a mesma política das outras tabelas, instâncias e disjuntor só existem no sistema.
ALTER TABLE burst_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE burst_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON burst_jobs FOR ALL TO PUBLIC
  USING (org_id = app_org_id() OR app_is_system())
  WITH CHECK (org_id = app_org_id() OR app_is_system());

ALTER TABLE burst_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE burst_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON burst_events FOR ALL TO PUBLIC
  USING (org_id = app_org_id() OR app_is_system())
  WITH CHECK (org_id = app_org_id() OR app_is_system());

ALTER TABLE burst_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE burst_instances FORCE ROW LEVEL SECURITY;
CREATE POLICY system_only ON burst_instances FOR ALL TO PUBLIC
  USING (app_is_system()) WITH CHECK (app_is_system());

ALTER TABLE burst_pool ENABLE ROW LEVEL SECURITY;
ALTER TABLE burst_pool FORCE ROW LEVEL SECURITY;
CREATE POLICY system_only ON burst_pool FOR ALL TO PUBLIC
  USING (app_is_system()) WITH CHECK (app_is_system());

-- --------------------------------------------------------------- privilégios
DO $$
BEGIN
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON burst_jobs, burst_events, burst_instances TO %I', app_role_name());
  EXECUTE format('GRANT SELECT, UPDATE ON burst_pool TO %I', app_role_name());
END $$;
