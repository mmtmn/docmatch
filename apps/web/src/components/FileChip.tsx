import { formatBytes, truncateMiddle } from '../lib/format';
import { t } from '../lib/i18n';
import { KIND_LABEL_KEY, type AcceptedFile } from '../lib/precheck';
import { CloseIcon, FileKindIcon } from './icons';
import styles from './FileChip.module.css';

export interface FileChipProps {
  file: AcceptedFile;
  onRemove: (id: string) => void;
}

export function FileChip({ file, onRemove }: FileChipProps) {
  return (
    <li className={styles.chip} data-kind={file.kind}>
      <span className={styles.icon}>
        <FileKindIcon kind={file.kind} size={22} />
      </span>
      <span className={styles.meta}>
        <span className={styles.name} title={file.name}>
          {truncateMiddle(file.name)}
        </span>
        <span className={styles.details}>
          {t(KIND_LABEL_KEY[file.kind])} · {formatBytes(file.size)}
        </span>
      </span>
      <button
        type="button"
        className={styles.remove}
        aria-label={t('dropzone.remove_file', { name: file.name })}
        onClick={() => onRemove(file.id)}
      >
        <CloseIcon size={18} />
      </button>
    </li>
  );
}
