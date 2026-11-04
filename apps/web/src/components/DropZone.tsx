import { useRef, type ChangeEvent } from 'react';
import { formatBytes } from '../lib/format';
import { t } from '../lib/i18n';
import { ACCEPT_ATTRIBUTE, type AcceptedFile, type RejectedFile } from '../lib/precheck';
import { FileChip } from './FileChip';
import { CloseIcon, UploadIcon } from './icons';
import styles from './DropZone.module.css';

export interface DropZoneProps {
  files: readonly AcceptedFile[];
  rejected: readonly RejectedFile[];
  /** True while a file is being dragged over the form (drops are handled by the parent). */
  dragging: boolean;
  /** Keyboard-open mode: a single line instead of the full list. */
  compact?: boolean;
  disabled?: boolean;
  onAdd: (files: File[]) => void;
  onRemove: (id: string) => void;
  onDismissRejection: (id: string) => void;
}

export function DropZone({
  files,
  rejected,
  dragging,
  compact = false,
  disabled = false,
  onAdd,
  onRemove,
  onDismissRejection,
}: DropZoneProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const open = (): void => inputRef.current?.click();
  const onInputChange = (event: ChangeEvent<HTMLInputElement>): void => {
    const list = event.target.files;
    if (list && list.length > 0) onAdd(Array.from(list));
    event.target.value = '';
  };

  const total = files.reduce((sum, file) => sum + file.size, 0);
  const countText =
    files.length === 1
      ? t('dropzone.files_count_one')
      : t('dropzone.files_count_many', { count: files.length });

  return (
    <section
      className={styles.zone}
      aria-label={t('dropzone.label')}
      data-dragging={dragging ? 'true' : undefined}
      data-compact={compact ? 'true' : undefined}
      data-empty={files.length === 0 ? 'true' : undefined}
    >
      <input
        ref={inputRef}
        type="file"
        multiple
        accept={ACCEPT_ATTRIBUTE}
        className="visually-hidden"
        tabIndex={-1}
        aria-hidden="true"
        disabled={disabled}
        onChange={onInputChange}
      />

      {files.length === 0 ? (
        <button type="button" className={styles.prompt} onClick={open} disabled={disabled}>
          <UploadIcon size={28} className={styles.promptIcon} />
          <span className={styles.promptTitle}>{t('dropzone.title')}</span>
          <span className={styles.promptHint}>{t('dropzone.hint')}</span>
          <span className={styles.promptShort}>{t('dropzone.hint_short')}</span>
          <span className={styles.promptAccepted}>{t('dropzone.accepted')}</span>
        </button>
      ) : (
        <div className={styles.filled}>
          <div className={styles.toolbar}>
            <p className={styles.summary}>
              {countText} · {t('dropzone.total_size', { size: formatBytes(total) })}
            </p>
            <button
              type="button"
              className={`btn btn--ghost ${styles.addMore}`}
              onClick={open}
              disabled={disabled}
            >
              {t('dropzone.add_more')}
            </button>
          </div>
          <ul className={styles.list} aria-label={t('dropzone.list_label')}>
            {files.map((file) => (
              <FileChip key={file.id} file={file} onRemove={onRemove} />
            ))}
          </ul>
        </div>
      )}

      <div className={styles.rejectedRegion} aria-live="assertive">
        {rejected.length > 0 && (
          <ul className={styles.rejected} aria-label={t('dropzone.rejected_title')}>
            {rejected.map((item) => (
              <li key={item.id} className={styles.rejectedItem}>
                <span>{item.reason}</span>
                <button
                  type="button"
                  className={styles.dismiss}
                  aria-label={t('dropzone.dismiss_rejection', { name: item.name })}
                  onClick={() => onDismissRejection(item.id)}
                >
                  <CloseIcon size={16} />
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <p className={styles.dragHint} aria-hidden="true">
        {t('dropzone.drag_active')}
      </p>
    </section>
  );
}
