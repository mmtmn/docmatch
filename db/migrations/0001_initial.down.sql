-- 0001_initial (down): remove tudo o que o up criou, na ordem inversa das
-- dependências. Extensões ficam (outros bancos do cluster podem usá-las).

DROP TABLE IF EXISTS media_assets CASCADE;
DROP TABLE IF EXISTS library_requests CASCADE;
DROP TABLE IF EXISTS source_fingerprints CASCADE;
DROP TABLE IF EXISTS tool_runs CASCADE;
DROP TABLE IF EXISTS tool_versions CASCADE;
DROP TABLE IF EXISTS tools CASCADE;
DROP TABLE IF EXISTS outbox CASCADE;
DROP TABLE IF EXISTS audit_log CASCADE;
DROP TABLE IF EXISTS geo_prices CASCADE;
DROP TABLE IF EXISTS model_prices CASCADE;
DROP TABLE IF EXISTS blog_posts CASCADE;
DROP TABLE IF EXISTS blog_topics CASCADE;
DROP TABLE IF EXISTS tenant_themes CASCADE;
DROP TABLE IF EXISTS customizations CASCADE;
DROP TABLE IF EXISTS api_usage CASCADE;
DROP TABLE IF EXISTS feedback CASCADE;
DROP TABLE IF EXISTS rule_uses CASCADE;
DROP TABLE IF EXISTS rules CASCADE;
DROP TABLE IF EXISTS matches CASCADE;
DROP TABLE IF EXISTS findings CASCADE;
DROP TABLE IF EXISTS uploads CASCADE;
DROP TABLE IF EXISTS runs CASCADE;
DROP TABLE IF EXISTS api_keys CASCADE;
DROP TABLE IF EXISTS workflows CASCADE;
DROP TABLE IF EXISTS quotes CASCADE;
DROP TABLE IF EXISTS credit_ledger CASCADE;
DROP TABLE IF EXISTS credit_holds CASCADE;
DROP TABLE IF EXISTS credit_grants CASCADE;
DROP TABLE IF EXISTS credit_accounts CASCADE;
DROP TABLE IF EXISTS stripe_events CASCADE;
DROP TABLE IF EXISTS subscriptions CASCADE;
DROP TABLE IF EXISTS magic_links CASCADE;
DROP TABLE IF EXISTS sessions CASCADE;
DROP TABLE IF EXISTS memberships CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS orgs CASCADE;
DROP TABLE IF EXISTS plans CASCADE;

DROP FUNCTION IF EXISTS ensure_month_partitions(int, int);
DROP FUNCTION IF EXISTS set_updated_at();

-- O papel da aplicação é do cluster (outros bancos e conexões abertas podem
-- usá-lo), então só os privilégios neste banco são revogados; o papel fica.
DO $$
DECLARE
  r text := app_role_name();
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', r);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I', r);
    EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM %I', r);
    EXECUTE format('REVOKE ALL ON SCHEMA public FROM %I', r);
  END IF;
END $$;

DROP FUNCTION IF EXISTS app_role_name();
DROP FUNCTION IF EXISTS app_is_system();
DROP FUNCTION IF EXISTS app_org_id();

DO $$
BEGIN
  -- Só remove o uuidv7() que este esquema criou (o substituto em public).
  IF EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = 'public' AND p.proname = 'uuidv7'
  ) THEN
    EXECUTE 'DROP FUNCTION public.uuidv7()';
  END IF;
END $$;
DROP FUNCTION IF EXISTS docmatch_uuidv7_fallback();
