import { useEffect, useId, useRef } from 'react';
import { trapDialogFocus } from '../lib/dialog';
import { t } from '../lib/i18n';
import styles from './projects/ProjectsApp.module.css';

export function ActionConfirmation({
  message,
  onAnswer,
}: {
  message: string;
  onAnswer: (confirmed: boolean) => void;
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
      aria-modal="true"
      onKeyDown={trapDialogFocus}
      onCancel={(event) => {
        event.preventDefault();
        onAnswer(false);
      }}
    >
      <h2 id={id}>{message}</h2>
      <div className={styles.actions}>
        <button className="btn btn--ghost" type="button" onClick={() => onAnswer(false)}>
          {t('common.cancel')}
        </button>
        <button className="btn btn--primary" type="button" onClick={() => onAnswer(true)}>
          {t('common.confirm')}
        </button>
      </div>
    </dialog>
  );
}
