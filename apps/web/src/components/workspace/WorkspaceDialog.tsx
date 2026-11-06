import { useEffect, useId, useRef } from 'react';
import { t } from '../../lib/i18n';
import { trapDialogFocus } from '../../lib/dialog';
import { WorkspacePanel } from './WorkspacePanel';
import styles from '../RunConfirmation.module.css';

export function WorkspaceDialog({ onClose }: { onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
  useEffect(() => {
    const dialog = ref.current;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog?.showModal();
    return () => {
      dialog?.close();
      opener?.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className={styles.dialog}
      aria-labelledby={id}
      onKeyDown={trapDialogFocus}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
    >
      <header className={styles.header}>
        <h2 id={id}>{t('workspace.open')}</h2>
        <button
          className={styles.close}
          type="button"
          onClick={onClose}
          aria-label={t('common.close')}
        >
          ×
        </button>
      </header>
      <WorkspacePanel expanded />
    </dialog>
  );
}
