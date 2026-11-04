import { useEffect, useRef } from 'react';
import { formatCurrency, formatShortDate, formatTimestamp } from '../../lib/format';
import { t } from '../../lib/i18n';
import { PlayIcon } from '../icons';
import type { Evidence, MediaEvidence, TableEvidence } from './types';
import styles from './EvidencePanel.module.css';

function TablePanel({ evidence, currency }: { evidence: TableEvidence; currency: string }) {
  return (
    <table className={styles.table}>
      <caption className="visually-hidden">{evidence.sourceName}</caption>
      <thead>
        <tr>
          <th scope="col">{t('results.col_date')}</th>
          <th scope="col">{t('results.col_description')}</th>
          <th scope="col" className={styles.amountHead}>
            {t('results.col_value')}
          </th>
          <th scope="col">{t('results.col_document')}</th>
        </tr>
      </thead>
      <tbody>
        {evidence.rows.map((row) => (
          <tr key={row.id} className={styles.row} data-cited={row.cited ? 'true' : undefined}>
            <td>
              {row.cited && (
                <span className="visually-hidden">{t('results.evidence_row_highlighted')}: </span>
              )}
              {formatShortDate(row.date)}
            </td>
            <td>{row.description}</td>
            <td className={styles.amount} data-negative={row.amount < 0 ? 'true' : undefined}>
              {formatCurrency(row.amount, currency)}
            </td>
            <td>{row.document ?? ''}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** Audio/video player that seeks to the cited timestamp as soon as metadata is available. */
export function MediaPlayer({ evidence }: { evidence: MediaEvidence }) {
  const ref = useRef<HTMLMediaElement | null>(null);

  const seek = (): void => {
    const media = ref.current;
    if (!media) return;
    try {
      media.currentTime = evidence.timestamp;
    } catch {
      // Not seekable yet; loadedmetadata will retry.
    }
  };

  useEffect(() => {
    const media = ref.current;
    if (!media) return undefined;
    const onLoaded = (): void => seek();
    media.addEventListener('loadedmetadata', onLoaded);
    if (media.readyState >= 1) seek();
    return () => media.removeEventListener('loadedmetadata', onLoaded);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [evidence.src, evidence.timestamp]);

  const at = formatTimestamp(evidence.timestamp);
  const common = {
    className: styles.media,
    controls: true,
    preload: 'metadata' as const,
    'data-testid': 'evidence-media',
  };

  return (
    <div className={styles.mediaWrap}>
      <p className={styles.mediaMeta}>{t('results.evidence_media_at', { time: at })}</p>
      {evidence.kind === 'video' ? (
        // Captions come with the transcript below; the media itself is evidence, not content.
        // eslint-disable-next-line jsx-a11y-x/media-has-caption
        <video ref={(el) => void (ref.current = el)} {...common} src={evidence.src ?? undefined}>
          {t('results.evidence_media_unsupported')}
        </video>
      ) : (
        // eslint-disable-next-line jsx-a11y-x/media-has-caption
        <audio ref={(el) => void (ref.current = el)} {...common} src={evidence.src ?? undefined}>
          {t('results.evidence_media_unsupported')}
        </audio>
      )}
      <button type="button" className={`btn btn--ghost ${styles.seek}`} onClick={seek}>
        <PlayIcon size={16} />
        {t('results.evidence_seek', { time: at })}
      </button>
      {evidence.transcript && (
        <blockquote className={styles.transcript}>{evidence.transcript}</blockquote>
      )}
    </div>
  );
}

export function EvidencePanel({ evidence, currency }: { evidence: Evidence; currency: string }) {
  return (
    <section className={styles.panel} aria-label={evidence.sourceName}>
      <h4 className={styles.source}>{evidence.sourceName}</h4>
      {evidence.kind === 'table' ? (
        <TablePanel evidence={evidence} currency={currency} />
      ) : (
        <MediaPlayer evidence={evidence} />
      )}
    </section>
  );
}
