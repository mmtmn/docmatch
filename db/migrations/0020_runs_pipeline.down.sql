-- Reverte 0020_runs_pipeline.

DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS rule_versions;

DROP INDEX IF EXISTS rules_org_status_idx;
DROP INDEX IF EXISTS rules_body_trgm_idx;
ALTER TABLE rules
  DROP COLUMN IF EXISTS promoted_at,
  DROP COLUMN IF EXISTS run_id,
  DROP COLUMN IF EXISTS provenance,
  DROP COLUMN IF EXISTS stats,
  DROP COLUMN IF EXISTS compiled,
  DROP COLUMN IF EXISTS applies_to,
  DROP COLUMN IF EXISTS scope,
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS version;

ALTER TABLE feedback DROP COLUMN IF EXISTS reason;
ALTER TABLE feedback DROP CONSTRAINT feedback_verdict_check;
ALTER TABLE feedback ADD CONSTRAINT feedback_verdict_check CHECK (verdict IN ('correct', 'incorrect', 'unsure'));

ALTER TABLE matches
  DROP COLUMN IF EXISTS explanation,
  DROP COLUMN IF EXISTS currency,
  DROP COLUMN IF EXISTS right_evidence,
  DROP COLUMN IF EXISTS left_evidence,
  DROP COLUMN IF EXISTS kind;

DROP INDEX IF EXISTS findings_run_severity_idx;
ALTER TABLE findings
  DROP COLUMN IF EXISTS updated_at,
  DROP COLUMN IF EXISTS feedback,
  DROP COLUMN IF EXISTS details,
  DROP COLUMN IF EXISTS source_ids,
  DROP COLUMN IF EXISTS match_id,
  DROP COLUMN IF EXISTS rule_id,
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS confidence,
  DROP COLUMN IF EXISTS code;
ALTER TABLE findings DROP CONSTRAINT findings_severity_check;
ALTER TABLE findings ADD CONSTRAINT findings_severity_check CHECK (severity IN ('low', 'medium', 'high', 'critical'));

DROP TABLE IF EXISTS run_events;
DROP FUNCTION IF EXISTS notify_run_event();

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

DROP INDEX IF EXISTS runs_workflow_idx;
ALTER TABLE runs
  DROP COLUMN IF EXISTS expires_at,
  DROP COLUMN IF EXISTS cancel_requested,
  DROP COLUMN IF EXISTS pending_quote_id,
  DROP COLUMN IF EXISTS error,
  DROP COLUMN IF EXISTS summary,
  DROP COLUMN IF EXISTS progress,
  DROP COLUMN IF EXISTS inputs,
  DROP COLUMN IF EXISTS last_seq,
  DROP COLUMN IF EXISTS credits_refunded,
  DROP COLUMN IF EXISTS credits_spent,
  DROP COLUMN IF EXISTS estimated_credits;
ALTER TABLE runs DROP CONSTRAINT runs_status_check;
-- Back to the old vocabulary (a run awaiting approval goes back to the queue).
SELECT set_config('app.system', 'on', true);
UPDATE runs SET status = CASE status WHEN 'completed' THEN 'succeeded' WHEN 'cancelled' THEN 'canceled' WHEN 'needs_approval' THEN 'queued' END
WHERE status IN ('completed', 'cancelled', 'needs_approval');
ALTER TABLE runs ADD CONSTRAINT runs_status_check CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled'));

DROP INDEX IF EXISTS quotes_run_idx;
ALTER TABLE quotes
  DROP COLUMN IF EXISTS kind,
  DROP COLUMN IF EXISTS run_id,
  DROP COLUMN IF EXISTS quote,
  DROP COLUMN IF EXISTS fingerprint;

ALTER TABLE uploads
  DROP COLUMN IF EXISTS expires_at,
  DROP COLUMN IF EXISTS completed_at,
  DROP COLUMN IF EXISTS ticket_expires_at,
  DROP COLUMN IF EXISTS duration_ms,
  DROP COLUMN IF EXISTS rejected_reason,
  DROP COLUMN IF EXISTS detected_mime,
  DROP COLUMN IF EXISTS role,
  DROP COLUMN IF EXISTS store,
  DROP COLUMN IF EXISTS kind;
