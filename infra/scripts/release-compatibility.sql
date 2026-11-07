-- Executed before replacing live binaries. Old releases do not enforce project
-- privacy or seat permissions. Never erase accepted work to permit a downgrade.
DO $$
DECLARE tab text; used boolean;
BEGIN
  IF current_setting('docmatch.release_account_context', true) = '1' THEN
    RETURN;
  END IF;
  FOREACH tab IN ARRAY ARRAY['projects','team_invitations','extension_grants'] LOOP
    IF to_regclass('public.' || tab) IS NOT NULL THEN
      EXECUTE format('SELECT EXISTS (SELECT 1 FROM public.%I)',tab) INTO used;
      IF used THEN
        RAISE EXCEPTION 'release incompatible with saved projects, seats or extension sessions; use a release with account-context support';
      END IF;
    END IF;
  END LOOP;
  IF to_regclass('public.memberships') IS NOT NULL THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM public.memberships WHERE role IN (''uploader'',''reconciler''))' INTO used;
    IF used THEN
      RAISE EXCEPTION 'release incompatible with seat permissions; use a release with account-context support';
    END IF;
  END IF;
END $$;
