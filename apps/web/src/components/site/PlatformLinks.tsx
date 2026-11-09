import { useEffect, useId, useRef, useState, useSyncExternalStore, type SubmitEvent } from 'react';
import type { Platform } from '../../lib/platformCatalog';
import { api, ApiError } from '../../lib/api';
import { limits, textLimits } from '../../lib/limits';
import { trapDialogFocus } from '../../lib/dialog';
import { t } from '../../lib/i18n';
import styles from './PlatformLinks.module.css';

const subscribeToHydration = () => () => {};
const clientReady = () => true;
const serverReady = () => false;

function PlatformIcon({ id }: { id: Platform['id'] }) {
  if (id === 'android')
    return <img src="/platforms/android-head.svg" width="28" height="17" alt="" />;
  if (id === 'github')
    return (
      <picture>
        <source
          media="(prefers-color-scheme: dark)"
          srcSet="/platforms/github-invertocat-white.svg"
        />
        <img src="/platforms/github-invertocat-black.svg" width="22" height="22" alt="" />
      </picture>
    );
  return null;
}
export function PlatformLinks({
  items,
  compact = false,
}: {
  items: Platform[];
  compact?: boolean;
}) {
  const ready = useSyncExternalStore(subscribeToHydration, clientReady, serverReady);
  const [notice, setNotice] = useState<Platform | null>(null);
  const [dialog, setDialog] = useState<{ platform: Platform; opener: HTMLButtonElement } | null>(
    null,
  );
  const Container = compact ? 'div' : 'section';
  return (
    <Container
      className={compact ? styles.compact : styles.section}
      aria-label={compact ? undefined : t('platforms.title')}
    >
      {!compact && <h2>{t('platforms.title')}</h2>}
      <ul className={styles.links}>
        {items
          .filter(
            (item) =>
              compact ||
              ['android', 'ios', 'github'].includes(item.id) ||
              item.status !== 'coming_soon',
          )
          .map((item) => (
            <li key={item.id}>
              {item.status === 'available' ? (
                <a href={item.url} className={styles.platform}>
                  <PlatformIcon id={item.id} />
                  {item.id === 'android' ? 'Android™' : item.name}
                </a>
              ) : (
                <button
                  type="button"
                  className={styles.platform}
                  disabled={!ready}
                  onClick={(event) =>
                    item.status === 'beta'
                      ? setDialog({ platform: item, opener: event.currentTarget })
                      : setNotice(item)
                  }
                >
                  <PlatformIcon id={item.id} />
                  <span>{item.id === 'android' ? 'Android™' : item.name}</span>
                  <span className={styles.state}>
                    {t(item.status === 'beta' ? 'platforms.beta' : 'platforms.soon')}
                  </span>
                </button>
              )}
            </li>
          ))}
      </ul>
      {compact && (
        <p className={styles.attribution}>
          {t('platforms.android_attribution')}{' '}
          <a href="https://creativecommons.org/licenses/by/3.0/">
            {t('platforms.android_license')}
          </a>
          .
        </p>
      )}
      {notice && (
        <div className={styles.notice} role="status">
          <p>{t('platforms.question', { name: notice.name })}</p>
          <div className={styles.actions}>
            <button
              type="button"
              onClick={(event) => setDialog({ platform: notice, opener: event.currentTarget })}
            >
              {t('platforms.notify')}
            </button>
            <button
              type="button"
              onClick={() => setNotice(null)}
              aria-label={t('platforms.dismiss')}
            >
              ×
            </button>
          </div>
        </div>
      )}
      {dialog && (
        <NoticeDialog
          platform={dialog.platform}
          opener={dialog.opener}
          close={() => setDialog(null)}
        />
      )}
    </Container>
  );
}
function NoticeDialog({
  platform,
  opener,
  close,
}: {
  platform: Platform;
  opener: HTMLButtonElement;
  close: () => void;
}) {
  const id = useId();
  const ref = useRef<HTMLDialogElement>(null);
  const pending = useRef<AbortController | null>(null);
  const [state, setState] = useState<'idle' | 'sending' | 'sent' | 'error'>('idle');
  const [error, setError] = useState('');
  useEffect(() => {
    const element = ref.current;
    element?.showModal();
    return () => {
      pending.current?.abort();
      pending.current = null;
      element?.close();
      opener.focus();
    };
  }, [opener]);
  const submit = async (event: SubmitEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (state === 'sending') return;
    const data = new FormData(event.currentTarget);
    const controller = new AbortController();
    pending.current?.abort();
    pending.current = controller;
    const deadline = window.setTimeout(
      () => controller.abort(),
      limits.http.request_timeout_s * 1000,
    );
    setState('sending');
    try {
      await api('/v1/platform-notices', {
        body: {
          platform: platform.id,
          email: typeof data.get('email') === 'string' ? data.get('email') : '',
          consent: data.get('consent') === 'on',
        },
        orgId: null,
        noRefresh: true,
        signal: controller.signal,
      });
      if (!controller.signal.aborted) setState('sent');
    } catch (cause) {
      if (pending.current !== controller) return;
      setError(
        cause instanceof ApiError && cause.status === 429
          ? t('platforms.try_later')
          : t('platforms.failed'),
      );
      setState('error');
    } finally {
      window.clearTimeout(deadline);
    }
  };
  return (
    <dialog
      ref={ref}
      className={styles.dialog}
      aria-labelledby={`${id}-title`}
      onKeyDown={trapDialogFocus}
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
    >
      <header>
        <h2 id={`${id}-title`}>
          {t(platform.status === 'beta' ? 'platforms.beta_title' : 'platforms.dialog_title', {
            name: platform.name,
          })}
        </h2>
        <button type="button" onClick={close} aria-label={t('common.close')}>
          ×
        </button>
      </header>
      {platform.status === 'beta' ? (
        <>
          <p>{t('platforms.beta_help')}</p>
          <a href={platform.url}>{t('platforms.open_beta')}</a>
        </>
      ) : state === 'sent' ? (
        <p role="status">{t('platforms.sent')}</p>
      ) : (
        <form
          onSubmit={(event) => {
            void submit(event);
          }}
        >
          <label htmlFor={`${id}-email`}>{t('platforms.email')}</label>
          <input
            id={`${id}-email`}
            name="email"
            type="email"
            autoComplete="email"
            required
            maxLength={textLimits.email}
            disabled={state === 'sending'}
          />
          <label className={styles.consent}>
            <input name="consent" type="checkbox" required disabled={state === 'sending'} />
            <span>{t('platforms.consent', { name: platform.name })}</span>
          </label>
          <p className={styles.help}>
            {t('platforms.privacy')} <a href="/privacidade">{t('footer.privacy')}</a>
          </p>
          {state === 'error' && <p role="alert">{error}</p>}
          <button className="btn btn--primary" type="submit" disabled={state === 'sending'}>
            {t(state === 'sending' ? 'platforms.sending' : 'platforms.send')}
          </button>
        </form>
      )}
    </dialog>
  );
}
