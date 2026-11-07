import { trapDialogFocus } from '../lib/dialog';
import { useEffect, useId, useRef } from 'react';
import { formatDuration } from '../lib/format';
import { t } from '../lib/i18n';
import type { QuoteView } from '../lib/pipeline';
import { CloseIcon } from './icons';
import styles from './RunConfirmation.module.css';

export function RunConfirmation({
  quote,
  exempt,
  context,
  onAnswer,
}: {
  quote: QuoteView;
  exempt: boolean;
  context?: { account: string; project: string };
  onAnswer: (confirmed: boolean) => void;
}) {
  const id = useId();
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (dialog && !dialog.open) {
      if (typeof dialog.showModal === 'function') dialog.showModal();
      else dialog.setAttribute('open', '');
    }
    return () => {
      dialog?.close?.();
      opener?.focus();
    };
  }, []);
  return (
    <dialog
      onKeyDown={trapDialogFocus}
      ref={ref}
      className={styles.dialog}
      aria-modal="true"
      aria-labelledby={`${id}-title`}
      aria-describedby={`${id}-description`}
      onCancel={(event) => {
        event.preventDefault();
        onAnswer(false);
      }}
    >
      <header className={styles.header}>
        <h2 id={`${id}-title`}>{t('app.confirm_title')}</h2>
        <button
          type="button"
          className={styles.close}
          aria-label={t('common.cancel')}
          onClick={() => onAnswer(false)}
        >
          <CloseIcon size={20} />
        </button>
      </header>
      <p id={`${id}-description`} className={styles.description}>
        {t('app.confirm_scope')}
      </p>
      {context && <p className={styles.description}>{t('projects.confirm_context', context)}</p>}
      {exempt ? (
        <p className={styles.exempt}>{t('app.confirm_exempt')}</p>
      ) : (
        <dl className={styles.estimate}>
          <div>
            <dt>{t('app.confirm_expected')}</dt>
            <dd>{t('app.confirm_tokens', { count: quote.expected_credits })}</dd>
          </div>
          <div>
            <dt>{t('app.confirm_cap')}</dt>
            <dd>{t('app.confirm_tokens', { count: quote.cap_credits })}</dd>
          </div>
        </dl>
      )}
      {quote.large && (quote.expected_seconds ?? 0) > 0 && (
        <p className={styles.description}>
          {t('app.confirm_large', { duration: formatDuration(quote.expected_seconds ?? 0) })}
        </p>
      )}
      <footer className={styles.actions}>
        <button type="button" className="btn btn--ghost" onClick={() => onAnswer(false)}>
          {t('app.confirm_cancel')}
        </button>
        <button type="button" className="btn btn--primary" onClick={() => onAnswer(true)}>
          {t('app.confirm_start')}
        </button>
      </footer>
    </dialog>
  );
}
