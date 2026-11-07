DROP TRIGGER memberships_keep_owner ON memberships;
DROP FUNCTION protect_team_owner();
-- Não converter permissões novas durante um rollback: retirar os vínculos
-- novos requer uma migração consciente, sem promover quem só envia documentos.
ALTER TABLE memberships DROP CONSTRAINT memberships_role_check;
ALTER TABLE memberships ADD CONSTRAINT memberships_role_check
  CHECK (role IN ('owner', 'admin', 'member', 'viewer'));
ALTER TABLE orgs DROP COLUMN kind;
