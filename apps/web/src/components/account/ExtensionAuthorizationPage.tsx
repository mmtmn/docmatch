import { useEffect, useState } from 'react';
import { t } from '../../lib/i18n';
import { errorMessage } from '../../lib/display';
import { currentUser, logout, type Me } from '../../lib/session';
import {
  authorizeExtension,
  extensionClient,
  readExtensionAuthorization,
} from '../../lib/extensionAuthorization';
import { AuthDialog } from '../AuthDialog';
import styles from '../projects/ProjectsApp.module.css';

export function ExtensionAuthorizationPage() {
  const [request] = useState(() =>
    typeof window === 'undefined' ? null : readExtensionAuthorization(window.location.search),
  );
  const [client, setClient] = useState<{ id: string; name: string } | null>(null);
  const [me, setMe] = useState<Me | null>(null);
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [authOpen, setAuthOpen] = useState(false);
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!request) return;
    const controller = new AbortController();
    void Promise.all([extensionClient(request, controller.signal), currentUser(controller.signal)])
      .then(([registered, user]) => {
        if (!controller.signal.aborted) {
          setClient(registered);
          setMe(user);
          setReady(true);
          setError(null);
        }
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [request, revision]);
  const allow = async () => {
    if (!request || !client || !me || busy) return;
    setBusy(true);
    setError(null);
    try {
      window.location.assign(await authorizeExtension(request));
    } catch (cause) {
      setError(errorMessage(cause));
      setBusy(false);
    }
  };
  const deny = () => {
    if (!request || !client) return;
    const target = new URL(request.redirect_uri);
    target.searchParams.set('error', 'access_denied');
    target.searchParams.set('state', request.state);
    window.location.assign(target.href);
  };
  const changeLogin = async () => {
    setBusy(true);
    setError(null);
    try {
      await logout();
      setMe(null);
      setAuthOpen(true);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className={styles.page}>
      <div className={styles.create}>
        <h1>{t('extensions.authorize_title')}</h1>
        {!request ? (
          <p role="alert">{t('extensions.invalid_request')}</p>
        ) : (
          <>
            {error && <p role="alert">{error}</p>}
            {!ready && !error && <p role="status">{t('mobile.loading')}</p>}
            {client && ready && (
              <>
                <h2>{client.name}</h2>
                <p>{t('extensions.permissions')}</p>
                <p>{t('extensions.disconnect_hint')}</p>
                {me ? (
                  <>
                    <p>{t('extensions.signed_in', { email: me.user.email })}</p>
                    <div className={styles.actions}>
                      <button
                        type="button"
                        className="btn btn--primary"
                        disabled={busy}
                        onClick={() => {
                          void allow();
                        }}
                      >
                        {t('extensions.allow')}
                      </button>
                      <button
                        type="button"
                        className="btn btn--ghost"
                        disabled={busy}
                        onClick={() => {
                          void changeLogin();
                        }}
                      >
                        {t('teams.switch_login')}
                      </button>
                    </div>
                  </>
                ) : (
                  <>
                    <p>{t('extensions.sign_in')}</p>
                    <button
                      type="button"
                      className="btn btn--primary"
                      disabled={busy}
                      onClick={() => setAuthOpen(true)}
                    >
                      {t('nav.login')}
                    </button>
                  </>
                )}
                <button type="button" className="btn btn--ghost" disabled={busy} onClick={deny}>
                  {t('extensions.deny')}
                </button>
              </>
            )}
          </>
        )}
      </div>
      <AuthDialog
        open={authOpen}
        onClose={() => setAuthOpen(false)}
        onSignedIn={() => {
          setAuthOpen(false);
          setRevision((value) => value + 1);
        }}
        returnTo={
          typeof window === 'undefined'
            ? '/extensoes/autorizar'
            : window.location.pathname + window.location.search
        }
      />
    </section>
  );
}
