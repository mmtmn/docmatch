import { useEffect, useState } from 'react';
import { api, ApiError } from '../../lib/api';
import type { Accounts } from '../../lib/accounts';
import { t } from '../../lib/i18n';
import { logout } from '../../lib/session';
import {
  previewInvitation,
  respondInvitation,
  teamError,
  type InvitationPreview,
} from '../../lib/teams';
import { AuthDialog } from '../AuthDialog';
import { projectDate } from '../../lib/display';
import styles from '../projects/ProjectsApp.module.css';

export function InvitationPage() {
  const [token] = useState(() =>
    typeof window === 'undefined'
      ? ''
      : (new URLSearchParams(window.location.hash.slice(1)).get('token') ?? ''),
  );
  const [preview, setPreview] = useState<InvitationPreview | null>(null);
  const [signedIn, setSignedIn] = useState(false);
  const [authOpen, setAuthOpen] = useState(false);
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [declined, setDeclined] = useState(false);
  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    const { signal } = controller;
    void (async () => {
      try {
        await api<Accounts>('/v1/me/organizations', { orgId: null, signal });
        if (!signal.aborted) setSignedIn(true);
      } catch (cause) {
        if (!(cause instanceof ApiError && cause.status === 401)) throw cause;
        if (!signal.aborted) setSignedIn(false);
      }
      const next = await previewInvitation(token, signal);
      if (!signal.aborted) {
        setPreview(next);
        setError(null);
      }
    })().catch((cause: unknown) => {
      if (!signal.aborted) setError(teamError(cause));
    });
    return () => controller.abort();
  }, [token, revision]);
  const signIn = async () => {
    setBusy(true);
    setError(null);
    try {
      if (signedIn) {
        await logout();
        setSignedIn(false);
      }
      setAuthOpen(true);
    } catch (cause) {
      setError(teamError(cause));
    } finally {
      setBusy(false);
    }
  };
  const answer = async (accept: boolean) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const result = await respondInvitation(token, accept);
      if (result.accepted) {
        window.location.assign(`/app?account=${encodeURIComponent(result.organization_id)}`);
      } else {
        setDeclined(true);
        window.history.replaceState(null, '', '/convite');
      }
    } catch (cause) {
      setError(teamError(cause));
    } finally {
      setBusy(false);
    }
  };
  const valid = preview?.status === 'pending' || preview?.status === 'accepted';
  return (
    <section className={styles.page}>
      <div className={styles.create}>
        <h1>{t('teams.invitation_title')}</h1>
        {!token && <p>{t('teams.invitation_missing')}</p>}
        {error && <p role="alert">{error}</p>}
        {token && !preview && !error && <p role="status">{t('mobile.loading')}</p>}
        {declined ? (
          <>
            <p role="status">{t('teams.declined_message')}</p>
            <a href="/app">{t('projects.title')}</a>
          </>
        ) : (
          preview && (
            <>
              <h2>{preview.organization}</h2>
              <p>{t('teams.invited_email', { email: preview.email_hint })}</p>
              <p>
                <strong>
                  {t(preview.role === 'uploader' ? 'teams.upload_only' : 'teams.full_access')}
                </strong>
              </p>
              <p>
                {t(
                  preview.role === 'uploader'
                    ? 'teams.upload_explanation'
                    : 'teams.full_explanation',
                )}
              </p>
              <p>{t('teams.personal_separate')}</p>
              {valid ? (
                <>
                  <a
                    className="btn btn--ghost"
                    href={`docmatch://convite#token=${encodeURIComponent(token)}`}
                  >
                    {t('teams.open_installed_app')}
                  </a>
                  {preview.status === 'pending' && (
                    <p className={styles.muted}>
                      {t('teams.expires', { date: projectDate(preview.expires_at) })}
                    </p>
                  )}
                  {preview.email_matches && signedIn ? (
                    <div className={styles.actions}>
                      <button
                        className="btn btn--primary"
                        disabled={busy}
                        onClick={() => {
                          void answer(true);
                        }}
                      >
                        {t(
                          preview.status === 'accepted'
                            ? 'teams.open_team'
                            : 'teams.accept_invitation',
                        )}
                      </button>
                      {preview.status === 'pending' && (
                        <button
                          className="btn btn--ghost"
                          disabled={busy}
                          onClick={() => {
                            void answer(false);
                          }}
                        >
                          {t('teams.decline_invitation')}
                        </button>
                      )}
                    </div>
                  ) : (
                    <>
                      <p>{t(signedIn ? 'teams.invite_email_mismatch' : 'teams.sign_in_invited')}</p>
                      <button
                        type="button"
                        className="btn btn--primary"
                        disabled={busy}
                        onClick={() => {
                          void signIn();
                        }}
                      >
                        {t(signedIn ? 'teams.switch_login' : 'nav.login')}
                      </button>
                    </>
                  )}
                </>
              ) : (
                <p role="status">
                  {t(preview.status === 'expired' ? 'teams.invite_expired' : 'teams.invite_used')}
                </p>
              )}
            </>
          )
        )}
      </div>
      <AuthDialog
        open={authOpen}
        onClose={() => setAuthOpen(false)}
        onSignedIn={() => {
          setAuthOpen(false);
          setRevision((value) => value + 1);
        }}
        returnTo={`/convite#token=${encodeURIComponent(token)}`}
      />
    </section>
  );
}
