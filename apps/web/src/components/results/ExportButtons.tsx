import { t } from '../../lib/i18n';
import { DownloadIcon } from '../icons';
import type { ExportFormat } from './types';
import styles from './ExportButtons.module.css';

export interface ExportButtonsProps {
  onExport: (format: ExportFormat) => void;
  status?: string | null;
}

export function ExportButtons({ onExport, status = null }: ExportButtonsProps) {
  return (
    <div className={styles.wrap} role="group" aria-label={t('results.export_heading')}>
      <button type="button" className="btn btn--ghost" onClick={() => onExport('csv')}>
        <DownloadIcon size={16} />
        {t('results.export_csv')}
      </button>
      <button type="button" className="btn btn--ghost" onClick={() => onExport('xlsx')}>
        <DownloadIcon size={16} />
        {t('results.export_xlsx')}
      </button>
      <button type="button" className="btn btn--ghost" onClick={() => onExport('pdf')}>
        <DownloadIcon size={16} />
        {t('results.export_pdf')}
      </button>
      <p className={styles.status} role="status" aria-live="polite">
        {status ?? ''}
      </p>
    </div>
  );
}
