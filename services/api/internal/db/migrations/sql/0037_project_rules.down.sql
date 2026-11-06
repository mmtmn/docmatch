-- Removing the boundary would expose learned private rules to legacy clients.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM rules WHERE project_id IS NOT NULL) THEN
  RAISE EXCEPTION 'project rules exist; retain schema and roll back the binary instead';
 END IF;
END $$;
DROP INDEX rules_project;
ALTER TABLE rules DROP CONSTRAINT rules_private_scope;
ALTER TABLE rules DROP CONSTRAINT rules_project_fk;
ALTER TABLE rules DROP COLUMN project_id;
