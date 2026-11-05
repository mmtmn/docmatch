-- Reverte 0021_run_timings.
DROP INDEX IF EXISTS outbox_dedupe_idx;
ALTER TABLE outbox DROP COLUMN IF EXISTS dedupe_key;
DROP TABLE IF EXISTS run_timings;
