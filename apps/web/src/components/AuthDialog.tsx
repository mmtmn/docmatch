// Sign-in dialog: only the methods this deployment offers (GET /v1/auth/methods) are shown, so a
// provider without keys never shows a button that fails. On a development API the local sign-in
// comes first. The draft stays in the browser; on success the caller resumes.
import { useEffect, useId, useRef, useState, type FormEvent } from 'react';
import { ApiError } from '../lib/api';
import { t } from '../lib/i18n';
import { textLimits } from '../lib/limits';
import {
  APP_PATH,
  authMethods,
  devLogin,
  startAppleLogin,
  startEmailLogin,
  startGoogleLogin,
  verifyEmailLogin,
  type AuthMethods,
  type Me,
} from '../lib/session';
import styles from './AuthDialog.module.css';

export interface AuthDialogProps {
  open: boolean;
  onClose: () => void;
  onSignedIn: (me: Me) => void;
  /** Site path to come back to after the Google or Apple round trip. */
  returnTo?: string;
  /** Rendered inline (the /login page) instead of as a modal. */
  inline?: boolean;
  /** Message shown above the options (an OAuth error brought back to /login). */
  notice?: string | null;
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

function message(err: unknown): string {
  return err instanceof ApiError || err instanceof Error ? err.message : t('errors.unknown');
}

export function AuthDialog({
  open,
  onClose,
  onSignedIn,
  returnTo = APP_PATH,
  inline = false,
  notice = null,
}: AuthDialogProps) {
  const id = useId();
  const ref = useRef<HTMLDialogElement>(null);
  const [methods, setMethods] = useState<AuthMethods | null>(null);
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [challenge, setChallenge] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(notice);

  useEffect(() => {
    if (inline) return;
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) {
      if (typeof dialog.showModal === 'function') dialog.showModal();
      else dialog.setAttribute('open', '');
    }
    if (!open && dialog.open) dialog.close();
  }, [open, inline]);

  useEffect(() => {
    if (!open) return undefined;
    const ctrl = new AbortController();
    authMethods(ctrl.signal)
      .then(setMethods)
      .catch(() => undefined);
    return () => ctrl.abort();
  }, [open]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  const sendCode = async (event: FormEvent) => {
    event.preventDefault();
    const address = email.trim();
    if (!EMAIL_RE.test(address)) {
      setError(t('errors.invalid_email'));
      return;
    }
    await run(async () => {
      const res = await startEmailLogin(address);
      setChallenge(res.challenge_id);
    });
  };

  const verify = async (event: FormEvent) => {
    event.preventDefault();
    if (!challenge || !/^\d{6}$/.test(code)) return;
    await run(async () => onSignedIn(await verifyEmailLogin(challenge, code)));
  };

  const provider = (start: (to: string) => Promise<void>, unavailable: string) => async () => {
    setBusy(true);
    setError(null);
    try {
      await start(returnTo);
    } catch (err) {
      setError(err instanceof ApiError && err.status === 503 ? unavailable : message(err));
      setBusy(false);
    }
  };

  const none =
    methods !== null && !methods.dev && !methods.google && !methods.apple && !methods.email;
  const showOr = Boolean(methods?.email && (methods.google || methods.apple || methods.dev));

  const body = (
    <>
      <div className={styles.header}>
        <h2 id={`${id}-title`} className={styles.title}>
          {inline ? t('auth.login_title') : t('auth.dialog_title')}
        </h2>
        {inline ? null : (
          <button
            type="button"
            className={styles.close}
            onClick={onClose}
            aria-label={t('common.close')}
          >
            ×
          </button>
        )}
      </div>
      <p className={styles.text}>{inline ? t('auth.login_text') : t('auth.dialog_text')}</p>

      {methods?.dev ? (
        <button
          type="button"
          className={styles.primary}
          disabled={busy}
          onClick={() => void run(async () => onSignedIn(await devLogin()))}
        >
          {busy ? t('auth.dev_signing_in') : t('auth.dev_sign_in')}
        </button>
      ) : null}
      {methods?.google ? (
        <button
          type="button"
          className={styles.google}
          onClick={() => void provider(startGoogleLogin, t('auth.google_unavailable'))()}
          disabled={busy}
        >
          {t('auth.sign_in_google')}
        </button>
      ) : null}
      {methods?.apple ? (
        <button
          type="button"
          className={styles.google}
          onClick={() => void provider(startAppleLogin, t('auth.apple_unavailable'))()}
          disabled={busy}
        >
          {t('auth.sign_in_apple')}
        </button>
      ) : null}
      {showOr ? <p className={styles.or}>{t('auth.or')}</p> : null}

      {methods?.email ? (
        challenge === null ? (
          <form onSubmit={(e) => void sendCode(e)} className={styles.form} noValidate>
            <label htmlFor={`${id}-email`}>{t('auth.email_label')}</label>
            <input
              id={`${id}-email`}
              type="email"
              autoComplete="email"
              inputMode="email"
              maxLength={textLimits.email}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
            />
            <button type="submit" className={styles.primary} disabled={busy}>
              {t('auth.send_code')}
            </button>
          </form>
        ) : (
          <form onSubmit={(e) => void verify(e)} className={styles.form} noValidate>
            <p className={styles.text}>{t('auth.code_sent', { email: email.trim() })}</p>
            <label htmlFor={`${id}-code`}>{t('auth.code_label')}</label>
            <input
              id={`${id}-code`}
              inputMode="numeric"
              autoComplete="one-time-code"
              pattern="\d{6}"
              maxLength={6}
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
              required
            />
            <button type="submit" className={styles.primary} disabled={busy || code.length !== 6}>
              {t('auth.verify')}
            </button>
            <button
              type="button"
              className={styles.link}
              onClick={() => {
                setChallenge(null);
                setCode('');
              }}
            >
              {t('auth.change_email')}
            </button>
          </form>
        )
      ) : null}
      {none ? <p className={styles.text}>{t('auth.none_available')}</p> : null}
      {error ? (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      ) : null}
    </>
  );

  if (inline) {
    return (
      <section className={styles.dialog} aria-labelledby={`${id}-title`}>
        {body}
      </section>
    );
  }
  return (
    <dialog
      ref={ref}
      className={styles.dialog}
      aria-labelledby={`${id}-title`}
      onClose={onClose}
      onCancel={onClose}
    >
      {body}
    </dialog>
  );
}
