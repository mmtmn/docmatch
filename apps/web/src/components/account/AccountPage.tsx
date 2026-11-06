import { WorkspaceShell } from '../workspace/WorkspaceShell';
import { WorkspacePanel } from '../workspace/WorkspacePanel';
// /conta: the signed-in account as every client sees it (the apps show the same data): profile,
// credits, and every device signed in to it, each of which can be signed out from here.
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../../lib/api';
import { t } from '../../lib/i18n';
import {
  currentUser,
  listDevices,
  logout,
  revokeDevice,
  signInLocally,
  type Device,
  type Me,
} from '../../lib/session';
import { AuthDialog } from '../AuthDialog';
import { BillingPanel } from './BillingPanel';
import { ApiKeysPanel } from './ApiKeysPanel';
import styles from './AccountPage.module.css';

type State =
  | { kind: 'loading' }
  | { kind: 'signed-out'; notice: string | null }
  | { kind: 'ready'; me: Me; devices: Device[]; notice: string | null };

const when = new Intl.DateTimeFormat('pt-BR', { dateStyle: 'short', timeStyle: 'short' });

function message(err: unknown): string {
  return err instanceof ApiError || err instanceof Error ? err.message : t('errors.unknown');
}

async function load(): Promise<State> {
  const me = (await currentUser()) ?? (await signInLocally());
  if (!me) return { kind: 'signed-out', notice: null };
  return { kind: 'ready', me, devices: await listDevices(), notice: null };
}

export function AccountPage() {
  const [state, setState] = useState<State>({ kind: 'loading' });
  const [busy, setBusy] = useState<string | null>(null);
  const request = useRef(0);

  const refresh = useCallback((notice: string | null = null) => {
    const current = ++request.current;
    load()
      .then((next) => {
        if (request.current === current)
          setState(next.kind === 'ready' ? { ...next, notice } : next);
      })
      .catch((err: unknown) => {
        if (request.current === current) setState({ kind: 'signed-out', notice: message(err) });
      });
  }, []);

  useEffect(() => {
    const lifecycle = request;
    refresh();
    const restore = (event: PageTransitionEvent) => {
      if (event.persisted) {
        setState({ kind: 'loading' });
        setBusy(null);
        refresh();
      }
    };
    window.addEventListener('pageshow', restore);
    return () => {
      lifecycle.current++;
      window.removeEventListener('pageshow', restore);
    };
  }, [refresh]);

  const revoke = async (d: Device) => {
    setBusy(d.id);
    try {
      await revokeDevice(d.id);
      if (d.current) {
        request.current++;
        setState({ kind: 'signed-out', notice: t('account.signed_out') });
        return;
      }
      refresh(t('account.revoked'));
    } catch (err) {
      setState((s) => (s.kind === 'ready' ? { ...s, notice: message(err) } : s));
    } finally {
      setBusy(null);
    }
  };

  const signOut = async () => {
    // A pending account refresh must never restore the account after logout.
    request.current++;
    setBusy('logout');
    try {
      await logout();
      setState({ kind: 'signed-out', notice: t('account.signed_out') });
    } catch (err) {
      setState((current) =>
        current.kind === 'ready' ? { ...current, notice: message(err) } : current,
      );
    } finally {
      setBusy(null);
    }
  };

  if (state.kind === 'loading') return <p role="status">{t('account.loading')}</p>;
  if (state.kind === 'signed-out') {
    return (
      <div className={styles.signIn}>
        <AuthDialog
          open
          inline
          onClose={() => undefined}
          onSignedIn={() => refresh()}
          notice={state.notice}
          noticeKind={state.notice === t('account.signed_out') ? 'status' : 'error'}
          returnTo={typeof window === 'undefined' ? '/conta' : `/conta${window.location.search}`}
        />
      </div>
    );
  }

  const { me, devices, notice } = state;
  return (
    <WorkspaceShell>
      <div className={styles.page}>
        <h1 className={styles.title}>{t('account.title')}</h1>
        {notice ? (
          <p role="status" className={styles.notice}>
            {notice}
          </p>
        ) : null}

        <section className={styles.card} aria-labelledby="conta-perfil">
          <h2 id="conta-perfil">{t('account.profile')}</h2>
          <dl className={styles.facts}>
            {me.user.display_name ? (
              <>
                <dt>{t('account.name_label')}</dt>
                <dd>{me.user.display_name}</dd>
              </>
            ) : null}
            <dt>{t('account.email_label')}</dt>
            <dd>{me.user.email}</dd>
            {me.credits ? (
              <>
                <dt>{t('account.credits')}</dt>
                <dd>
                  {me.credits.token_exempt
                    ? t('account.token_exempt')
                    : t('account.credits_available', { available: me.credits.available })}
                </dd>
              </>
            ) : null}
          </dl>
        </section>

        <BillingPanel onRefresh={() => refresh()} />
        <ApiKeysPanel />
        <WorkspacePanel />

        <section className={styles.card} aria-labelledby="conta-aparelhos">
          <h2 id="conta-aparelhos">{t('account.devices')}</h2>
          <p className={styles.muted}>{t('account.devices_text')}</p>
          <ul className={styles.devices}>
            {devices.map((d) => (
              <li key={d.id} className={styles.device}>
                <div>
                  <strong>{d.name}</strong>
                  {d.current ? (
                    <span className={styles.badge}>{t('account.this_device')}</span>
                  ) : null}
                  <div className={styles.muted}>
                    {t('account.last_seen', { when: when.format(new Date(d.last_seen_at)) })}
                  </div>
                </div>
                <button
                  type="button"
                  className={styles.secondary}
                  disabled={busy !== null}
                  aria-label={t('account.revoke_label', { name: d.name })}
                  onClick={() => void revoke(d)}
                >
                  {t('account.revoke')}
                </button>
              </li>
            ))}
          </ul>
        </section>

        <button
          type="button"
          className={styles.primary}
          disabled={busy !== null}
          onClick={() => void signOut()}
        >
          {t('account.sign_out')}
        </button>
      </div>
    </WorkspaceShell>
  );
}
