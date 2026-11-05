// /login: where the API sends OAuth errors (?error=<code>) and where a signed-out visitor lands.
// The sign-in options are the dialog's, inline; on success the app opens.
import { useEffect, useState } from 'react';
import { t, type MessageKey } from '../../lib/i18n';
import { APP_PATH, currentUser } from '../../lib/session';
import { AuthDialog } from '../AuthDialog';

const NOTICES: Record<string, MessageKey> = {
  oauth_state_mismatch: 'auth.error_oauth_state_mismatch',
  oauth_provider_error: 'auth.error_oauth_provider_error',
  oauth_email_unverified: 'auth.error_oauth_email_unverified',
  account_suspended: 'auth.error_account_suspended',
  account_deletion_pending: 'auth.error_account_deletion_pending',
  service_unavailable: 'auth.error_service_unavailable',
};

/** The message for an OAuth error code brought back in the URL. */
export function noticeFor(code: string | null): string | null {
  if (!code) return null;
  return t(NOTICES[code] ?? 'auth.error_generic');
}

function errorCode(): string | null {
  return typeof window === 'undefined'
    ? null
    : new URLSearchParams(window.location.search).get('error');
}

function openApp(): void {
  window.location.assign(APP_PATH);
}

// Rendered with client:only (it reads the URL on the first render).
export function LoginPage() {
  const [notice] = useState(() => noticeFor(errorCode()));

  useEffect(() => {
    // Already signed in on this device: straight to the app.
    currentUser()
      .then((me) => {
        if (me) openApp();
      })
      .catch(() => undefined);
  }, []);

  return <AuthDialog open inline onClose={() => undefined} onSignedIn={openApp} notice={notice} />;
}
