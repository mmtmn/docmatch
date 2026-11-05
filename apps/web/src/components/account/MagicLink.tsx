// /login/email?challenge_id=<id>&code=<token>: the link in the sign-in e-mail. It confirms the
// token with the API (same endpoint as the typed code) and opens the app.
import { useEffect, useState } from 'react';
import { ApiError } from '../../lib/api';
import { t } from '../../lib/i18n';
import { APP_PATH, verifyMagicLink } from '../../lib/session';

interface LinkParams {
  challengeId: string;
  token: string;
}

function readParams(): LinkParams | null {
  if (typeof window === 'undefined') return null;
  const q = new URLSearchParams(window.location.search);
  const challengeId = q.get('challenge_id');
  const token = q.get('code');
  return challengeId && token ? { challengeId, token } : null;
}

// Rendered with client:only (it reads the URL on the first render).
export function MagicLink() {
  const [params] = useState(readParams);
  const [failure, setFailure] = useState<string | null>(() =>
    params ? null : t('auth.magic_failed', { message: t('auth.error_generic') }),
  );

  useEffect(() => {
    if (!params) return;
    verifyMagicLink(params.challengeId, params.token)
      .then(() => window.location.assign(APP_PATH))
      .catch((err: unknown) => {
        const message =
          err instanceof ApiError || err instanceof Error ? err.message : t('errors.unknown');
        setFailure(t('auth.magic_failed', { message }));
      });
  }, [params]);

  if (failure) {
    return (
      <section>
        <p role="alert">{failure}</p>
        <p>
          <a href="/login">{t('auth.magic_back')}</a>
        </p>
      </section>
    );
  }
  return <p role="status">{t('auth.magic_title')}</p>;
}
