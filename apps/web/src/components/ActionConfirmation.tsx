import { useEffect, useId, useRef } from 'react';
import { trapDialogFocus } from '../lib/dialog';
import { t } from '../lib/i18n';
import styles from './projects/ProjectsApp.module.css';

export function ActionConfirmation({
  message,
  onAnswer,
  confirmLabel = t('common.confirm'),
  description,
}: {
  message: string;
  onAnswer: (confirmed: boolean) => void;
  confirmLabel?: string;
  description?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
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
      ref={ref}
      className={styles.dialog}
      aria-labelledby={id}
      aria-describedby={description ? `${id}-description` : undefined}
      aria-modal="true"
      onKeyDown={trapDialogFocus}
      onCancel={(event) => {
        event.preventDefault();
        onAnswer(false);
      }}
    >
      <h2 id={id}>{message}</h2>
      {description && <p id={`${id}-description`}>{description}</p>}
      <div className={styles.actions}>
        <button className="btn btn--ghost" type="button" onClick={() => onAnswer(false)}>
          {t('common.cancel')}
        </button>
        <button className="btn btn--primary" type="button" onClick={() => onAnswer(true)}>
          {confirmLabel}
        </button>
      </div>
    </dialog>
  );
}
