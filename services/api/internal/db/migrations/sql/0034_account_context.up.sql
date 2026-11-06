-- Contas existentes continuam pessoais até a conversão explícita pelo dono.
ALTER TABLE orgs ADD COLUMN kind text NOT NULL DEFAULT 'personal'
  CHECK (kind IN ('personal', 'team'));
ALTER TABLE memberships DROP CONSTRAINT memberships_role_check;
ALTER TABLE memberships ADD CONSTRAINT memberships_role_check
  CHECK (role IN ('owner', 'admin', 'member', 'viewer', 'uploader', 'reconciler'));

-- O bloqueio da linha da conta serializa remoções e transferências concorrentes.
CREATE FUNCTION protect_team_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE team_kind text;
BEGIN
  IF OLD.role <> 'owner' OR (TG_OP = 'UPDATE' AND NEW.role = 'owner') THEN
    RETURN OLD;
  END IF;
  SELECT kind INTO team_kind FROM orgs WHERE id=OLD.org_id AND deleted_at IS NULL FOR UPDATE;
  IF team_kind = 'team' AND NOT EXISTS (
    SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id
    WHERE m.org_id=OLD.org_id AND m.user_id<>OLD.user_id AND m.role='owner'
    AND u.deleted_at IS NULL AND u.deletion_requested_at IS NULL
  ) THEN
    RAISE EXCEPTION 'transferir a responsabilidade antes de sair da equipe' USING ERRCODE='23514';
  END IF;
  RETURN OLD;
END $$;
CREATE TRIGGER memberships_keep_owner AFTER DELETE OR UPDATE OF role ON memberships
  FOR EACH ROW EXECUTE FUNCTION protect_team_owner();
