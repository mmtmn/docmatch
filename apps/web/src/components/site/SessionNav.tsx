import { useEffect, useState } from 'react';
import { t } from '../../lib/i18n';
import { APP_PATH, currentUser, SESSION_CHANGED_EVENT } from '../../lib/session';
import styles from './SessionNav.module.css';

/** Static fallback links still work before hydration; the label follows the HttpOnly session. */
export function SessionNav({ path }: { path: string }) {
  const [signedIn, setSignedIn] = useState(false);
  const onLogin = path === '/login' || path.startsWith('/login/');
  const onAccount = path === '/conta';

  useEffect(() => {
    if (onLogin) return;
    let controller: AbortController | undefined;
    const check = () => {
      controller?.abort();
      controller = new AbortController();
      const { signal } = controller;
      currentUser(signal)
        .then((me) => {
          if (!signal.aborted) setSignedIn(me !== null);
        })
        .catch(() => undefined);
    };
    const restore = (event: PageTransitionEvent) => {
      if (event.persisted) check();
    };
    check();
    window.addEventListener(SESSION_CHANGED_EVENT, check);
    window.addEventListener('focus', check);
    window.addEventListener('pageshow', restore);
    return () => {
      controller?.abort();
      window.removeEventListener(SESSION_CHANGED_EVENT, check);
      window.removeEventListener('focus', check);
      window.removeEventListener('pageshow', restore);
    };
  }, [onLogin]);

  const goToApp = onLogin || (signedIn && onAccount);
  const href = goToApp ? APP_PATH : signedIn ? '/conta' : '/login';
  const label = goToApp ? t('nav.app') : signedIn ? t('nav.account') : t('nav.login');

  return (
    <a className={`btn btn--primary ${styles.link}`} href={href}>
      {label}
    </a>
  );
}
