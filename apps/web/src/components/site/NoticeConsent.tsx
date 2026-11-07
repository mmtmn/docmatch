import { useEffect, useRef, useState } from 'react';
import { api } from '../../lib/api';
import { t } from '../../lib/i18n';
import { limits } from '../../lib/limits';

function readLink(): { action: 'confirm' | 'unsubscribe'; token: string } | null {
  const values = new URLSearchParams(window.location.hash.slice(1));
  const action = values.has('confirmar') ? 'confirm' : 'unsubscribe';
  const token = values.get(action === 'confirm' ? 'confirmar' : 'cancelar') ?? '';
  return values.size === 1 && /^[A-Za-z0-9_-]{43,256}$/.test(token) ? { action, token } : null;
}
export function NoticeConsent() {
  // This island is client-only: fragment tokens never reach SSR or a server log.
  const [link, setLink] = useState(readLink);
  const pending = useRef<AbortController | null>(null);
  const [state, setState] = useState<'ready' | 'invalid' | 'sending' | 'done' | 'error'>(
    link ? 'ready' : 'invalid',
  );
  useEffect(() => {
    window.history.replaceState(null, '', window.location.pathname);
    const changed = () => {
      if (!window.location.hash) return;
      pending.current?.abort();
      pending.current = null;
      const next = readLink();
      setLink(next);
      setState(next ? 'ready' : 'invalid');
      window.history.replaceState(null, '', window.location.pathname);
    };
    window.addEventListener('hashchange', changed);
    return () => {
      window.removeEventListener('hashchange', changed);
      pending.current?.abort();
      pending.current = null;
    };
  }, []);
  const submit = async () => {
    if (!link || state === 'sending') return;
    setState('sending');
    const controller = new AbortController();
    pending.current = controller;
    const deadline = window.setTimeout(
      () => controller.abort(),
      limits.http.request_timeout_s * 1000,
    );
    try {
      await api(`/v1/platform-notices/${link.action}`, {
        body: { token: link.token },
        orgId: null,
        noRefresh: true,
        signal: controller.signal,
      });
      if (pending.current === controller) setState('done');
    } catch {
      if (pending.current === controller) setState('error');
    } finally {
      window.clearTimeout(deadline);
    }
  };
  if (state === 'invalid') return <p role="status">{t('platforms.invalid')}</p>;
  if (state === 'done')
    return (
      <p role="status">
        {t(link?.action === 'confirm' ? 'platforms.confirmed' : 'platforms.cancelled')}
      </p>
    );
  return (
    <>
      <p>{t(link?.action === 'confirm' ? 'platforms.confirm_help' : 'platforms.cancel_help')}</p>
      {state === 'error' && <p role="alert">{t('platforms.link_error')}</p>}
      <button
        className="btn btn--primary"
        type="button"
        disabled={state === 'sending'}
        onClick={() => {
          void submit();
        }}
      >
        {t(
          state === 'sending'
            ? 'platforms.sending'
            : link?.action === 'confirm'
              ? 'platforms.confirm'
              : 'platforms.cancel',
        )}
      </button>
    </>
  );
}
