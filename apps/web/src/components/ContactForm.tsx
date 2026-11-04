import { useId, useState, type SubmitEvent } from 'react';
import { useCharLimit } from '../hooks/useCharLimit';
import { apiUrl } from '../lib/env';
import { t } from '../lib/i18n';
import { limits, textLimits } from '../lib/limits';
import { CharCounter } from './CharCounter';
import styles from './ContactForm.module.css';

const CONTACT_PATH = '/v1/contact';
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/;
/** The browser gives up when the API would too (limits.yaml http.request_timeout_s). */
const REQUEST_TIMEOUT_MS = limits.http.request_timeout_s * 1000;

function requestSignal(): AbortSignal | null {
  // AbortSignal.timeout is missing in a few older engines; the request then simply has no timeout.
  return typeof AbortSignal.timeout === 'function' ? AbortSignal.timeout(REQUEST_TIMEOUT_MS) : null;
}

type Status =
  { kind: 'idle' } | { kind: 'sending' } | { kind: 'ok' } | { kind: 'error'; message: string };

export function ContactForm() {
  const id = useId();
  const [name, setName] = useState('');
  const [email, setEmail] = useState('');
  const [message, setMessage] = useState('');
  const [website, setWebsite] = useState(''); // honeypot, hidden from people
  const [errors, setErrors] = useState<{ name?: string; email?: string; message?: string }>({});
  const [status, setStatus] = useState<Status>({ kind: 'idle' });

  const nameLimit = useCharLimit(name, textLimits.display_name);
  const emailLimit = useCharLimit(email, textLimits.email);
  const messageLimit = useCharLimit(message, textLimits.contact_message);

  const validate = (): boolean => {
    const next: typeof errors = {};
    if (name.trim() === '') next.name = t('errors.required');
    if (email.trim() === '') next.email = t('errors.required');
    else if (!EMAIL_PATTERN.test(email.trim())) next.email = t('errors.invalid_email');
    if (message.trim() === '') next.message = t('errors.required');
    setErrors(next);
    return Object.keys(next).length === 0;
  };

  const onSubmit = async (event: SubmitEvent<HTMLFormElement>): Promise<void> => {
    event.preventDefault();
    // Enter or a second click while a request is in flight must not post the message twice.
    if (status.kind === 'sending') return;
    if (!validate()) return;
    if (website !== '') {
      // Bots fill the hidden field; pretend it worked.
      setStatus({ kind: 'ok' });
      return;
    }
    const url = apiUrl(CONTACT_PATH);
    if (!url) {
      setStatus({ kind: 'error', message: t('contact.api_unconfigured') });
      return;
    }
    setStatus({ kind: 'sending' });
    try {
      const response = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ name: name.trim(), email: email.trim(), message: message.trim() }),
        signal: requestSignal(),
      });
      if (!response.ok) {
        setStatus({
          kind: 'error',
          message: t('errors.server', { status: String(response.status) }),
        });
        return;
      }
      setStatus({ kind: 'ok' });
      setName('');
      setEmail('');
      setMessage('');
    } catch (error) {
      const timedOut = error instanceof DOMException && error.name === 'TimeoutError';
      setStatus({ kind: 'error', message: t(timedOut ? 'errors.timeout' : 'errors.network') });
    }
  };

  const sending = status.kind === 'sending';

  return (
    <form className={styles.form} noValidate onSubmit={(event) => void onSubmit(event)}>
      <div className="field">
        <label className="field__label" htmlFor={`${id}-name`}>
          {t('contact.name_label')}
        </label>
        <input
          id={`${id}-name`}
          className="input"
          type="text"
          name="name"
          autoComplete="name"
          required
          maxLength={textLimits.display_name}
          value={name}
          aria-invalid={errors.name ? 'true' : undefined}
          aria-describedby={`${id}-name-error ${id}-name-counter`}
          onChange={(event) => setName(event.target.value)}
        />
        <p id={`${id}-name-error`} className={styles.error}>
          {errors.name ?? ''}
        </p>
        <CharCounter id={`${id}-name-counter`} state={nameLimit} />
      </div>

      <div className="field">
        <label className="field__label" htmlFor={`${id}-email`}>
          {t('contact.email_label')}
        </label>
        <input
          id={`${id}-email`}
          className="input"
          type="email"
          name="email"
          autoComplete="email"
          required
          maxLength={textLimits.email}
          value={email}
          aria-invalid={errors.email ? 'true' : undefined}
          aria-describedby={`${id}-email-error ${id}-email-counter`}
          onChange={(event) => setEmail(event.target.value)}
        />
        <p id={`${id}-email-error`} className={styles.error}>
          {errors.email ?? ''}
        </p>
        <CharCounter id={`${id}-email-counter`} state={emailLimit} />
      </div>

      <div className="field">
        <label className="field__label" htmlFor={`${id}-message`}>
          {t('contact.message_label')}
        </label>
        <textarea
          id={`${id}-message`}
          className="textarea"
          name="message"
          rows={6}
          required
          maxLength={textLimits.contact_message}
          value={message}
          aria-invalid={errors.message ? 'true' : undefined}
          aria-describedby={`${id}-message-help ${id}-message-error ${id}-message-counter`}
          onChange={(event) => setMessage(event.target.value)}
        />
        <p id={`${id}-message-help`} className="field__help">
          {t('contact.message_help')}
        </p>
        <p id={`${id}-message-error`} className={styles.error}>
          {errors.message ?? ''}
        </p>
        <CharCounter id={`${id}-message-counter`} state={messageLimit} />
      </div>

      <div className={styles.honeypot} aria-hidden="true">
        <label htmlFor={`${id}-website`}>{t('common.optional')}</label>
        <input
          id={`${id}-website`}
          type="text"
          name="website"
          tabIndex={-1}
          autoComplete="off"
          value={website}
          onChange={(event) => setWebsite(event.target.value)}
        />
      </div>

      <div className={styles.actions}>
        <button
          type="submit"
          className="btn btn--primary"
          aria-disabled={sending ? 'true' : undefined}
        >
          {sending ? t('contact.sending') : t('contact.submit')}
        </button>
        <p className={styles.status} role="status" aria-live="polite">
          {status.kind === 'ok'
            ? t('contact.success')
            : status.kind === 'error'
              ? status.message
              : ''}
        </p>
      </div>
    </form>
  );
}
