import { trapDialogFocus } from '../lib/dialog';
// Sign-in dialog: only the methods this deployment offers (GET /v1/auth/methods) are shown, so a
// provider without keys never shows a button that fails. On a development API the local sign-in
// comes first. The draft stays in the browser; on success the caller resumes.
import { useEffect, useId, useRef, useState, type SubmitEvent } from 'react';
import { ApiError } from '../lib/api';
import { t } from '../lib/i18n';
import { textLimits } from '../lib/limits';
import {
  APP_PATH,
  authMethods,
  devLogin,
  passwordLogin,
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
  noticeKind?: 'error' | 'status';
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

function message(err: unknown): string {
  return err instanceof ApiError || err instanceof Error ? err.message : t('errors.unknown');
}

// A fresh form for every opening: an old code, password or pending request must not survive it.
export function AuthDialog(props: AuthDialogProps) {
  return props.open ? <AuthForm {...props} /> : null;
}

function AuthForm({
  open,
  onClose,
  onSignedIn,
  returnTo = APP_PATH,
  inline = false,
  notice = null,
  noticeKind = 'error',
}: AuthDialogProps) {
  const id = useId();
  const ref = useRef<HTMLDialogElement>(null);
  const [methods, setMethods] = useState<AuthMethods | null>(null);
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [withPassword, setWithPassword] = useState(false);
  const [withEmailCode, setWithEmailCode] = useState(false);
  const [showPassword, setShowPassword] = useState(false);
  const [code, setCode] = useState('');
  const [challenge, setChallenge] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [noticeDismissed, setNoticeDismissed] = useState(false);
  const [retry, setRetry] = useState(0);
  const attempt = useRef(0);

  useEffect(() => {
    const lifecycle = attempt;
    const restore = (event: PageTransitionEvent) => {
      if (!event.persisted) return;
      lifecycle.current++;
      setBusy(false);
      setPassword('');
      setShowPassword(false);
      setError(null);
    };
    window.addEventListener('pageshow', restore);
    return () => {
      lifecycle.current++;
      window.removeEventListener('pageshow', restore);
    };
  }, []);

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
      .then((available) => {
        if (!ctrl.signal.aborted) setMethods(available);
      })
      .catch(() => undefined);
    return () => ctrl.abort();
  }, [open, retry]);

  const run = async <T,>(fn: () => Promise<T>, done: (value: T) => void) => {
    const current = ++attempt.current;
    setBusy(true);
    setError(null);
    setNoticeDismissed(true);
    try {
      const value = await fn();
      if (attempt.current === current) done(value);
    } catch (err) {
      if (attempt.current === current) setError(message(err));
    } finally {
      if (attempt.current === current) setBusy(false);
    }
  };

  const sendCode = async (event: SubmitEvent<HTMLFormElement>) => {
    event.preventDefault();
    const address = email.trim();
    if (!EMAIL_RE.test(address)) {
      setError(t('errors.invalid_email'));
      return;
    }
    await run(
      () => startEmailLogin(address),
      (res) => setChallenge(res.challenge_id),
    );
  };

  const verify = async (event: SubmitEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!challenge || !/^\d{6}$/.test(code)) return;
    await run(() => verifyEmailLogin(challenge, code), onSignedIn);
  };

  const provider = (start: (to: string) => Promise<void>, unavailable: string) => async () => {
    const current = ++attempt.current;
    setBusy(true);
    setError(null);
    setNoticeDismissed(true);
    try {
      await start(returnTo);
    } catch (err) {
      if (attempt.current !== current) return;
      setError(err instanceof ApiError && err.status === 503 ? unavailable : message(err));
      setBusy(false);
    }
  };

  const none =
    methods !== null &&
    !methods.dev &&
    !methods.google &&
    !methods.apple &&
    !methods.email &&
    !methods.password;
  const showOr = Boolean(
    (methods?.email || methods?.password) && (methods.google || methods.apple || methods.dev),
  );
  const Title = inline ? 'h1' : 'h2';

  const body = (
    <>
      <div className={styles.mark} aria-hidden="true">
        <svg viewBox="0 0 32 32" fill="none">
          <rect x="5" y="4" width="16" height="22" rx="3" />
          <path d="M11 11h8M11 16h5m3 7 3 3 6-7" />
        </svg>
      </div>
      <div className={styles.header}>
        <Title id={`${id}-title`} className={styles.title}>
          {inline ? t('auth.login_title') : t('auth.dialog_title')}
        </Title>
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

      {!noticeDismissed && notice ? (
        <p
          role={noticeKind === 'status' ? 'status' : 'alert'}
          className={noticeKind === 'status' ? styles.notice : styles.error}
        >
          {notice}
        </p>
      ) : null}
      {methods === null ? (
        <p role="status" className={styles.loading}>
          {t('auth.loading_methods')}
        </p>
      ) : null}

      <div className={styles.methods}>
        {methods?.dev ? (
          <button
            type="button"
            className={styles.primary}
            disabled={busy}
            onClick={() => void run(() => devLogin(), onSignedIn)}
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
            <svg className={styles.providerIcon} aria-hidden="true" viewBox="0 0 24 24">
              <path
                fill="#4285F4"
                d="M21.6 12.23c0-.71-.06-1.39-.18-2.05H12v3.88h5.38a4.6 4.6 0 0 1-2 3.02v2.51h3.24c1.89-1.74 2.98-4.3 2.98-7.36Z"
              />
              <path
                fill="#34A853"
                d="M12 22c2.7 0 4.96-.9 6.62-2.41l-3.24-2.51c-.9.6-2.05.96-3.38.96-2.6 0-4.8-1.76-5.59-4.12H3.06v2.59A10 10 0 0 0 12 22Z"
              />
              <path
                fill="#FBBC05"
                d="M6.41 13.92a6 6 0 0 1 0-3.84V7.49H3.06a10 10 0 0 0 0 9.02l3.35-2.59Z"
              />
              <path
                fill="#EA4335"
                d="M12 5.96c1.47 0 2.79.5 3.83 1.5L18.7 4.6A9.6 9.6 0 0 0 12 2a10 10 0 0 0-8.94 5.49l3.35 2.59C7.2 7.72 9.4 5.96 12 5.96Z"
              />
            </svg>
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

        {methods?.password ? (
          <button
            type="button"
            className={styles.secondary}
            disabled={busy}
            aria-expanded={withPassword}
            onClick={() => {
              setWithPassword(!withPassword);
              setWithEmailCode(false);
              setPassword('');
              setShowPassword(false);
              setChallenge(null);
              setCode('');
              setError(null);
            }}
          >
            {withPassword ? t('auth.other_methods') : t('auth.use_password')}
          </button>
        ) : null}
        {methods?.password && withPassword ? (
          <form
            className={styles.form}
            onSubmit={(event) => {
              event.preventDefault();
              void run(
                () => passwordLogin(email.trim(), password),
                (me) => {
                  setPassword('');
                  setShowPassword(false);
                  onSignedIn(me);
                },
              );
            }}
          >
            <label htmlFor={`${id}-password-email`}>{t('auth.email_label')}</label>
            <input
              id={`${id}-password-email`}
              type="email"
              autoComplete="username"
              value={email}
              maxLength={textLimits.email}
              required
              disabled={busy}
              onChange={(event) => setEmail(event.target.value)}
            />
            <label htmlFor={`${id}-password`}>{t('auth.password_label')}</label>
            <div className={styles.passwordField}>
              <input
                id={`${id}-password`}
                type={showPassword ? 'text' : 'password'}
                autoComplete="current-password"
                value={password}
                maxLength={textLimits.password_max}
                required
                disabled={busy}
                onChange={(event) => setPassword(event.target.value)}
              />
              <button
                type="button"
                className={styles.passwordToggle}
                aria-controls={`${id}-password`}
                aria-pressed={showPassword}
                disabled={busy}
                onClick={() => setShowPassword((value) => !value)}
              >
                {showPassword ? t('auth.hide_password') : t('auth.show_password')}
              </button>
            </div>
            <button type="submit" className={styles.primary} disabled={busy}>
              {busy ? t('auth.signing_in') : t('nav.login')}
            </button>
          </form>
        ) : null}
        {methods?.email && !withPassword && !withEmailCode ? (
          <button
            type="button"
            className={styles.secondary}
            disabled={busy}
            onClick={() => setWithEmailCode(true)}
          >
            {t('auth.use_email_code')}
          </button>
        ) : null}
        {methods?.email && !withPassword && withEmailCode ? (
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
                disabled={busy}
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
                disabled={busy}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                required
              />
              <button type="submit" className={styles.primary} disabled={busy || code.length !== 6}>
                {t('auth.verify')}
              </button>
              <button
                type="button"
                className={styles.link}
                disabled={busy}
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
      </div>
      {busy ? (
        <p role="status" className={styles.loading}>
          {t('auth.signing_in')}
        </p>
      ) : null}
      {none ? (
        <div className={styles.unavailable}>
          <p>{t('auth.none_available')}</p>
          <button
            type="button"
            className={styles.secondary}
            onClick={() => {
              setMethods(null);
              setRetry((value) => value + 1);
            }}
          >
            {t('auth.retry')}
          </button>
        </div>
      ) : null}
      {error ? (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      ) : null}
      <p className={styles.legal}>
        <a href="/termos">{t('footer.terms')}</a>
        <span aria-hidden="true">·</span>
        <a href="/privacidade">{t('footer.privacy')}</a>
      </p>
    </>
  );

  if (inline) {
    return (
      <section className={`${styles.dialog} ${styles.inline}`} aria-labelledby={`${id}-title`}>
        {body}
      </section>
    );
  }
  return (
    <dialog
      onKeyDown={trapDialogFocus}
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
