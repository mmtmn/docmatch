-- Reverte 0010_auth_sessions.
DROP TABLE IF EXISTS account_exports;
DROP TABLE IF EXISTS tenant_keys;
DROP INDEX IF EXISTS users_deletion_due_idx;
ALTER TABLE users
  DROP COLUMN IF EXISTS notifications,
  DROP COLUMN IF EXISTS suspended_at,
  DROP COLUMN IF EXISTS deletion_requested_at,
  DROP COLUMN IF EXISTS deletion_scheduled_for;
ALTER TABLE magic_links
  DROP COLUMN IF EXISTS code_hash,
  DROP COLUMN IF EXISTS attempts;
ALTER TABLE sessions
  DROP COLUMN IF EXISTS access_hash,
  DROP COLUMN IF EXISTS access_expires_at,
  DROP COLUMN IF EXISTS rotated_at,
  DROP COLUMN IF EXISTS rotations;
