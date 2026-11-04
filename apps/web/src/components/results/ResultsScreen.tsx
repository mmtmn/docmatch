import { useCallback, useMemo, useRef, useState } from 'react';
import { downloadText, toCsv } from '../../lib/csv';
import { formatCurrency, formatShortDate } from '../../lib/format';
import { t } from '../../lib/i18n';
import { ConfidenceBadge, confidenceLabel } from './ConfidenceBadge';
import { ExportButtons } from './ExportButtons';
import { FindingDrawer, MATCH_TYPE_LABEL, STATUS_LABEL } from './FindingDrawer';
import type { ExportFormat, Finding, FindingStatus, RunResults } from './types';
import styles from './ResultsScreen.module.css';

export interface ResultsScreenProps {
  results: RunResults;
  /** Called with the decisions; the API client will persist them (plan phase: API). */
  onDecision?: (id: string, status: FindingStatus, note?: string) => void;
  /** XLSX/PDF are produced server-side; CSV is built here. */
  onExport?: (format: Exclude<ExportFormat, 'csv'>) => Promise<void> | void;
}

/** Exceptions first, biggest amount at risk first; ties keep the API order. */
export function sortByRisk(findings: readonly Finding[]): Finding[] {
  return [...findings].sort((a, b) => b.amountAtRisk - a.amountAtRisk);
}

export function findingsCsv(findings: readonly Finding[]): string {
  const header = [
    t('results.col_finding'),
    t('results.col_amount'),
    t('results.col_confidence'),
    t('results.col_status'),
    t('results.explanation'),
  ];
  const rows = findings.map((finding) => [
    finding.title,
    finding.amountAtRisk,
    confidenceLabel(finding.confidence),
    t(STATUS_LABEL[finding.status]),
    finding.explanation,
  ]);
  return toCsv(header, rows);
}

export function ResultsScreen({ results, onDecision, onExport }: ResultsScreenProps) {
  const [findings, setFindings] = useState<Finding[]>(() => sortByRisk(results.findings));
  const [openId, setOpenId] = useState<string | null>(null);
  const [showClean, setShowClean] = useState(false);
  const [exportStatus, setExportStatus] = useState<string | null>(null);
  const openerRef = useRef<HTMLElement | null>(null);

  const open = findings.find((finding) => finding.id === openId) ?? null;

  const closeDrawer = useCallback((): void => {
    setOpenId(null);
    openerRef.current?.focus();
  }, []);

  const decide = useCallback(
    (id: string, status: FindingStatus, note?: string): void => {
      setFindings((prev) =>
        prev.map((finding) =>
          finding.id === id
            ? { ...finding, status, ...(note !== undefined ? { note } : {}) }
            : finding,
        ),
      );
      onDecision?.(id, status, note);
    },
    [onDecision],
  );

  const exportAs = async (format: ExportFormat): Promise<void> => {
    if (format === 'csv') {
      const name = `docmatch-${results.runId}.csv`;
      downloadText(name, findingsCsv(findings));
      setExportStatus(t('results.export_done', { name }));
      return;
    }
    if (onExport) {
      await onExport(format);
      return;
    }
    setExportStatus(t('results.export_soon', { format: format.toUpperCase() }));
  };

  const cleanTotal = useMemo(
    () => results.cleanMatches.reduce((sum, item) => sum + item.amount, 0),
    [results.cleanMatches],
  );

  return (
    <div className={styles.screen}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>{t('results.title')}</h1>
          <p className={styles.subtitle}>
            {t('results.subtitle', {
              exceptions: findings.length,
              clean: results.cleanMatches.length,
              files: results.fileCount,
            })}
          </p>
          {results.demo && (
            <p className="notice notice--info small" role="note">
              {t('results.demo_notice')}
            </p>
          )}
        </div>
        <ExportButtons onExport={(format) => void exportAs(format)} status={exportStatus} />
      </header>

      <section className={styles.section} aria-labelledby="divergencias">
        <div className={styles.sectionHead}>
          <h2 id="divergencias" className={styles.sectionTitle}>
            {t('results.exceptions_heading')}
          </h2>
          <p className="small muted">{t('results.sorted_by_risk')}</p>
        </div>
        {findings.length === 0 ? (
          <p className="muted">{t('results.exceptions_empty')}</p>
        ) : (
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th scope="col">{t('results.col_finding')}</th>
                  <th scope="col" className={styles.amountHead}>
                    {t('results.col_amount')}
                  </th>
                  <th scope="col">{t('results.col_confidence')}</th>
                  <th scope="col">{t('results.col_status')}</th>
                </tr>
              </thead>
              <tbody>
                {findings.map((finding) => (
                  <tr key={finding.id} data-status={finding.status} data-testid="finding-row">
                    <td>
                      <button
                        type="button"
                        className={styles.rowButton}
                        onClick={(event) => {
                          openerRef.current = event.currentTarget;
                          setOpenId(finding.id);
                        }}
                      >
                        <span className={styles.rowTitle}>{finding.title}</span>
                        <span className={styles.rowKicker}>
                          {t(MATCH_TYPE_LABEL[finding.matchType])}
                        </span>
                      </button>
                    </td>
                    <td className={styles.amount}>
                      {formatCurrency(finding.amountAtRisk, finding.currency)}
                    </td>
                    <td>
                      <ConfidenceBadge level={finding.confidence} />
                    </td>
                    <td>
                      <span className={styles.status} data-status={finding.status}>
                        {t(STATUS_LABEL[finding.status])}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className={styles.section} aria-labelledby="limpas">
        <div className={styles.cleanRow}>
          <h2 id="limpas" className={styles.cleanTitle}>
            {t('results.clean_summary', { count: results.cleanMatches.length })}
            <span className={styles.cleanTotal}>
              {' '}
              · {formatCurrency(cleanTotal, results.cleanMatches[0]?.currency ?? 'BRL')}
            </span>
          </h2>
          {results.cleanMatches.length > 0 && (
            <button
              type="button"
              className="btn btn--ghost"
              aria-expanded={showClean}
              aria-controls="limpas-lista"
              onClick={() => setShowClean((value) => !value)}
            >
              {showClean ? t('results.clean_hide') : t('results.clean_show')}
            </button>
          )}
        </div>
        {results.cleanMatches.length === 0 && <p className="muted">{t('results.clean_empty')}</p>}
        {showClean && (
          <div className={styles.tableWrap} id="limpas-lista">
            <table className={styles.table}>
              <thead>
                <tr>
                  <th scope="col">{t('results.col_date')}</th>
                  <th scope="col">{t('results.col_description')}</th>
                  <th scope="col" className={styles.amountHead}>
                    {t('results.col_value')}
                  </th>
                  <th scope="col">{t('results.sources')}</th>
                </tr>
              </thead>
              <tbody>
                {results.cleanMatches.map((item) => (
                  <tr key={item.id}>
                    <td>{formatShortDate(item.date)}</td>
                    <td>{item.description}</td>
                    <td className={styles.amount}>{formatCurrency(item.amount, item.currency)}</td>
                    <td className={styles.sources}>
                      {item.leftSource} ↔ {item.rightSource}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <p>
        <a className="btn btn--ghost" href="/app">
          {t('results.back_to_form')}
        </a>
      </p>

      {open && (
        <FindingDrawer key={open.id} finding={open} onClose={closeDrawer} onDecision={decide} />
      )}
    </div>
  );
}
