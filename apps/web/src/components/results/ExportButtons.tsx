import { t } from '../../lib/i18n';
import { DownloadIcon } from '../icons';
import type { ExportFormat } from './types';
import styles from './ExportButtons.module.css';

export interface ExportButtonsProps {
  preferred?: ExportFormat;
  onExport: (format: ExportFormat) => void;
  status?: string | null;
  busy?: boolean;
  corrected?: boolean;
  available?: boolean;
  failed?: boolean;
  createHref?: string;
}

export function ExportButtons({
  onExport,
  preferred = 'xlsx',
  status = null,
  busy = false,
  corrected = false,
  available = true,
  failed = false,
  createHref = '/app',
}: ExportButtonsProps) {
  return (
    <div className={styles.wrap} role="group" aria-label={t('results.export_heading')}>
      <div className={styles.actions}>
        {corrected ? (
          <button
            type="button"
            disabled={busy || !available}
            className="btn btn--primary"
            onClick={() => onExport('xlsx')}
          >
            <DownloadIcon size={18} />
            {t('results.download_corrected')}
          </button>
        ) : (
          <a className="btn btn--primary" href={createHref}>
            {t('results.generate_corrected')}
          </a>
        )}
        {available && (
          <button
            type="button"
            disabled={busy}
            className="btn btn--ghost"
            onClick={() => onExport('pdf')}
          >
            {t('results.download_report')}
          </button>
        )}
      </div>
      {available && preferred !== 'xlsx' && (
        <button
          type="button"
          className="btn btn--ghost"
          disabled={busy}
          onClick={() => onExport(preferred)}
        >
          {t('workspace.preferred_export', { format: preferred.toUpperCase() })}
        </button>
      )}
      <details className={styles.formats}>
        <summary>{t('results.other_formats')}</summary>
        <div className={styles.actions}>
          {!corrected && available && (
            <button
              type="button"
              disabled={busy}
              className="btn btn--ghost"
              onClick={() => onExport('xlsx')}
            >
              {t('results.download_findings_xlsx')}
            </button>
          )}
          <button
            type="button"
            disabled={busy}
            className="btn btn--ghost"
            onClick={() => onExport('csv')}
          >
            {t(corrected ? 'results.download_data_csv' : 'results.download_findings_csv')}
          </button>
        </div>
      </details>
      <p
        className={styles.status}
        data-error={failed}
        role={failed ? 'alert' : 'status'}
        aria-live="polite"
      >
        {status ?? ''}
      </p>
    </div>
  );
}
