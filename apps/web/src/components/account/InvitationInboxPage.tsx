import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../../lib/api';
import { t } from '../../lib/i18n';
import { projectDate } from '../../lib/display';
import { SESSION_CHANGED_EVENT } from '../../lib/session';
import {
  pendingInvitations,
  respondPendingInvitation,
  teamError,
  type InvitationPreview,
} from '../../lib/teams';
import { ActionConfirmation } from '../ActionConfirmation';
import { AuthDialog } from '../AuthDialog';
import styles from '../projects/ProjectsApp.module.css';

export function InvitationInboxPage() {
  const [items, setItems] = useState<InvitationPreview[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [signedOut, setSignedOut] = useState(false);
  const [auth, setAuth] = useState(false);
  const [revision, setRevision] = useState(0);
  const [confirmation, setConfirmation] = useState<{
    invitation: InvitationPreview;
    accept: boolean;
  } | null>(null);
  const operation = useRef<AbortController | null>(null);
  const locked = useRef(false);
  const reload = useCallback(() => {
    operation.current?.abort();
    setBusy(true);
    setError('');
    setItems([]);
    setCursor(undefined);
    setRevision((value) => value + 1);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    operation.current = controller;
    pendingInvitations(undefined, controller.signal)
      .then((page) => {
        if (controller.signal.aborted) return;
        setItems(page.items);
        setCursor(page.next_cursor);
        setSignedOut(false);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (cause instanceof ApiError && cause.status === 401) setSignedOut(true);
        else setError(teamError(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    const changed = () => {
      setConfirmation(null);
      setNotice('');
      reload();
    };
    window.addEventListener(SESSION_CHANGED_EVENT, changed);
    return () => {
      controller.abort();
      window.removeEventListener(SESSION_CHANGED_EVENT, changed);
    };
  }, [revision, reload]);
  const more = async () => {
    if (!cursor || locked.current || busy) return;
    locked.current = true;
    setBusy(true);
    setError('');
    const signal = operation.current?.signal;
    try {
      const page = await pendingInvitations(cursor, signal);
      if (signal?.aborted) return;
      setItems((old) => [
        ...old,
        ...page.items.filter((item) => !old.some((v) => v.id === item.id)),
      ]);
      setCursor(page.next_cursor);
    } catch (cause) {
      if (!signal?.aborted) setError(teamError(cause));
    } finally {
      locked.current = false;
      if (!signal?.aborted) setBusy(false);
    }
  };
  const answer = async () => {
    if (!confirmation || locked.current || busy) return;
    locked.current = true;
    setBusy(true);
    setError('');
    const signal = operation.current?.signal;
    const { invitation, accept } = confirmation;
    setConfirmation(null);
    try {
      const result = await respondPendingInvitation(invitation.id, accept);
      if (signal?.aborted) return;
      setItems((old) => old.filter((item) => item.id !== invitation.id));
      // Refresh memberships without switching the user's selected account.
      window.dispatchEvent(new Event(SESSION_CHANGED_EVENT));
      setNotice(t(result.accepted ? 'teams.inbox_accepted' : 'teams.declined_message'));
    } catch (cause) {
      if (!signal?.aborted) setError(teamError(cause));
    } finally {
      locked.current = false;
      if (!signal?.aborted) setBusy(false);
    }
  };
  return (
    <section className={styles.page}>
      <h1>{t('teams.inbox_title')}</h1>
      <p>{t('teams.inbox_hint')}</p>
      <p>{t('teams.personal_separate')}</p>
      {notice && <p role="status">{notice}</p>}
      {error && <p role="alert">{error}</p>}
      {busy && <p role="status">{t('mobile.loading')}</p>}
      {signedOut ? (
        <button className="btn btn--primary" onClick={() => setAuth(true)}>
          {t('nav.login')}
        </button>
      ) : (
        <>
          {!busy && !error && items.length === 0 && (
            <p>
              {t('teams.inbox_empty')} {t('teams.inbox_verify')}
            </p>
          )}
          {items.map((invitation) => (
            <article key={invitation.id} className={styles.create}>
              <h2>{invitation.organization}</h2>
              <p>
                <strong>
                  {t(invitation.role === 'uploader' ? 'teams.upload_only' : 'teams.full_access')}
                </strong>
              </p>
              <p>
                {t(
                  invitation.role === 'uploader'
                    ? 'teams.upload_explanation'
                    : 'teams.full_explanation',
                )}
              </p>
              <p>{t('teams.expires', { date: projectDate(invitation.expires_at) })}</p>
              <div className={styles.actions}>
                <button
                  className="btn btn--primary"
                  disabled={busy}
                  onClick={() => setConfirmation({ invitation, accept: true })}
                >
                  {t('teams.accept_invitation')}
                </button>
                <button
                  className="btn btn--ghost"
                  disabled={busy}
                  onClick={() => setConfirmation({ invitation, accept: false })}
                >
                  {t('teams.decline_invitation')}
                </button>
              </div>
            </article>
          ))}
          {cursor && (
            <button className="btn btn--ghost" disabled={busy} onClick={() => void more()}>
              {t('teams.inbox_more')}
            </button>
          )}
        </>
      )}
      {error && (
        <button className="btn btn--ghost" disabled={busy} onClick={reload}>
          {t('projects.retry_load')}
        </button>
      )}
      <a href="/app">{t('projects.title')}</a>
      {confirmation && (
        <ActionConfirmation
          message={t(confirmation.accept ? 'teams.inbox_accept' : 'teams.inbox_decline', {
            name: confirmation.invitation.organization,
          })}
          onAnswer={(accepted) => {
            if (accepted) void answer();
            else setConfirmation(null);
          }}
        />
      )}
      <AuthDialog
        open={auth}
        onClose={() => setAuth(false)}
        onSignedIn={() => {
          setAuth(false);
          reload();
        }}
        returnTo="/convites"
      />
    </section>
  );
}
