// Sign-in dialog shown when "Conciliar" needs an account: Google, or a 6-digit code by e-mail.
// The draft stays in the browser; on success the caller resumes the run.
import { useEffect, useId, useRef, useState, type FormEvent } from 'react';
import { ApiError } from '../lib/api';
import { t } from '../lib/i18n';
import { textLimits } from '../lib/limits';
import { startEmailLogin, startGoogleLogin, verifyEmailLogin, type Me } from '../lib/session';
import styles from './AuthDialog.module.css';

export interface AuthDialogProps {
  open: boolean;
  onClose: () => void;
  onSignedIn: (me: Me) => void;
  /** Site path to come back to after the Google round trip. */
  returnTo?: string;
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

function message(err: unknown): string {
  return err instanceof ApiError || err instanceof Error ? err.message : t('errors.unknown');
}

export function AuthDialog({ open, onClose, onSignedIn, returnTo = '/app' }: AuthDialogProps) {
  const id = useId();
  const ref = useRef<HTMLDialogElement>(null);
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [challenge, setChallenge] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) {
      if (typeof dialog.showModal === 'function') dialog.showModal();
      else dialog.setAttribute('open', '');
    }
    if (!open && dialog.open) dialog.close();
  }, [open]);

  const sendCode = async (event: FormEvent) => {
    event.preventDefault();
    const address = email.trim();
    if (!EMAIL_RE.test(address)) {
      setError(t('errors.invalid_email'));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await startEmailLogin(address);
      setChallenge(res.challenge_id);
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  const verify = async (event: FormEvent) => {
    event.preventDefault();
    if (!challenge || !/^\d{6}$/.test(code)) return;
    setBusy(true);
    setError(null);
    try {
      onSignedIn(await verifyEmailLogin(challenge, code));
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  const google = async () => {
    setBusy(true);
    setError(null);
    try {
      await startGoogleLogin(returnTo);
    } catch (err) {
      setError(
        err instanceof ApiError && err.status === 503 ? t('auth.google_unavailable') : message(err),
      );
      setBusy(false);
    }
  };

  return (
    <dialog
      ref={ref}
      className={styles.dialog}
      aria-labelledby={`${id}-title`}
      onClose={onClose}
      onCancel={onClose}
    >
      <div className={styles.header}>
        <h2 id={`${id}-title`} className={styles.title}>
          {t('auth.dialog_title')}
        </h2>
        <button
          type="button"
          className={styles.close}
          onClick={onClose}
          aria-label={t('common.close')}
        >
          ×
        </button>
      </div>
      <p className={styles.text}>{t('auth.dialog_text')}</p>

      <button type="button" className={styles.google} onClick={() => void google()} disabled={busy}>
        {t('auth.sign_in_google')}
      </button>
      <p className={styles.or}>{t('auth.or')}</p>

      {challenge === null ? (
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
      )}
      {error ? (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      ) : null}
    </dialog>
  );
}
