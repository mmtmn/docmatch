-- Reverte 0022_burst_workers.
DROP TABLE IF EXISTS burst_pool;
DROP TABLE IF EXISTS burst_instances;
DROP TABLE IF EXISTS burst_events;
DROP TABLE IF EXISTS burst_jobs;
