import { defaultManifest, useWorkspace } from '../../lib/workspace';
import { WorkspaceShell } from '../workspace/WorkspaceShell';
import { WorkspacePanel } from '../workspace/WorkspacePanel';
import { useCallback, useMemo, useRef, useState } from 'react';
import { downloadText, toCsv } from '../../lib/csv';
import { formatCurrency, formatShortDate } from '../../lib/format';
import { t } from '../../lib/i18n';
import { FileKindIcon, WarningIcon } from '../icons';
import { confidenceLabel } from './ConfidenceBadge';
import { Disclosure } from './Disclosure';
import { ExportButtons } from './ExportButtons';
import { DocumentChanges, DocumentPending, ReconciledDocument } from './ReconciledDocument';
import { FindingDrawer, STATUS_LABEL } from './FindingDrawer';
import type { ExportFormat, Finding, FindingStatus, RunResults } from './types';
import styles from './ResultsScreen.module.css';

export interface ResultsScreenProps {
  results: RunResults;
  backHref?: string;
  onDecision?: (id: string, status: FindingStatus, note?: string) => Promise<void> | void;
  allowUndo?: boolean;
  canDecide?: boolean;
  onExport?: (format: ExportFormat) => Promise<void> | void;
}
const priority: Record<string, number> = { critical: 5, high: 4, medium: 3, low: 2, info: 1 };
/** Severity precedes amount: a critical warning must not disappear below a large informational match. */
export function sortByRisk(findings: readonly Finding[]): Finding[] {
  return [...findings].sort(
    (a, b) =>
      (priority[b.severity ?? ''] ?? 2) - (priority[a.severity ?? ''] ?? 2) ||
      b.amountAtRisk - a.amountAtRisk,
  );
}
export function findingsCsv(findings: readonly Finding[]): string {
  return toCsv(
    [
      t('results.col_finding'),
      t('results.col_amount'),
      t('results.col_confidence'),
      t('results.col_status'),
      t('results.explanation'),
    ],
    findings.map((f) => [
      f.title,
      f.amountAtRisk,
      confidenceLabel(f.confidence),
      t(STATUS_LABEL[f.status]),
      f.explanation,
    ]),
  );
}

function FindingsTable({
  items,
  onOpen,
}: {
  items: Finding[];
  onOpen: (finding: Finding, opener: HTMLElement) => void;
}) {
  return (
    <div className={styles.tableWrap}>
      <table className={styles.table}>
        <thead>
          <tr>
            <th scope="col">{t('results.col_finding')}</th>
            <th scope="col">{t('results.col_amount')}</th>
            <th scope="col">{t('results.col_status')}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((f) => (
            <tr key={f.id} data-status={f.status} data-testid="finding-row">
              <td>
                <button
                  type="button"
                  className={styles.rowButton}
                  onClick={(event) => onOpen(f, event.currentTarget)}
                >
                  <span className={styles.rowTitle}>{f.title}</span>
                  <span className={styles.rowKicker}>
                    {f.severity === 'info'
                      ? t('results.findings_information')
                      : t('results.findings_review')}
                  </span>
                </button>
              </td>
              <td className={styles.amount} data-label={t('results.col_amount')}>
                {formatCurrency(f.amountAtRisk, f.currency)}
              </td>
              <td className={styles.status} data-label={t('results.col_status')}>
                {t(STATUS_LABEL[f.status])}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function ResultsScreen({
  results,
  onDecision,
  onExport,
  allowUndo = true,
  canDecide = true,
  backHref = '/app',
}: ResultsScreenProps) {
  const workspace = useWorkspace();
  const manifest = workspace?.manifest ?? defaultManifest;
  const [decisions, setDecisions] = useState<
    Record<string, { status: FindingStatus; note?: string | undefined }>
  >({});
  const findings = useMemo(
    () =>
      sortByRisk(results.findings)
        .sort((a, b) =>
          manifest.sort_by === 'amount_at_risk'
            ? b.amountAtRisk - a.amountAtRisk
            : manifest.sort_by === 'confidence'
              ? { high: 3, medium: 2, low: 1 }[b.confidence] -
                { high: 3, medium: 2, low: 1 }[a.confidence]
              : 0,
        )
        .map((f) => {
          const decision = decisions[f.id];
          return decision
            ? {
                ...f,
                status: decision.status,
                ...(decision.note !== undefined ? { note: decision.note } : {}),
              }
            : f;
        }),
    [results.findings, decisions, manifest.sort_by],
  );
  const [openId, setOpenId] = useState<string | null>(null);
  const [exportStatus, setExportStatus] = useState<string | null>(null);
  const [exportBusy, setExportBusy] = useState(false);
  const [exportFailed, setExportFailed] = useState(false);
  const exportLock = useRef(false);
  const openerRef = useRef<HTMLElement | null>(null);
  const headingRef = useRef<HTMLHeadingElement | null>(null);
  const open = findings.find((f) => f.id === openId) ?? null;
  const doc = results.document;
  const corrected = Boolean(doc && (doc.receivables.length || doc.bank_entries.length));
  const pending = findings.filter((f) => f.severity !== 'info' && f.status === 'open');
  // Review feedback alone never resolves the underlying critical condition.
  const critical = findings.filter((f) => f.severity === 'critical');
  const closeDrawer = useCallback(() => {
    setOpenId(null);
    if (openerRef.current?.isConnected) openerRef.current.focus();
    else headingRef.current?.focus();
  }, []);
  const decide = useCallback(
    async (id: string, status: FindingStatus, note?: string) => {
      await onDecision?.(id, status, note);
      setDecisions((prev) => ({ ...prev, [id]: { status, note: note ?? prev[id]?.note } }));
    },
    [onDecision],
  );
  const exportAs = async (format: ExportFormat) => {
    if (exportLock.current) return;
    exportLock.current = true;
    setExportBusy(true);
    setExportFailed(false);
    setExportStatus(t('results.export_preparing'));
    try {
      if (onExport) await onExport(format);
      else if (format === 'csv')
        downloadText(`docmatch-${results.runId}.csv`, findingsCsv(findings));
      else return;
      setExportStatus(t('results.export_done', { name: `docmatch-${results.runId}.${format}` }));
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        setExportStatus(null);
        return;
      }
      setExportFailed(true);
      setExportStatus(err instanceof Error ? err.message : t('errors.unknown'));
    } finally {
      exportLock.current = false;
      setExportBusy(false);
    }
  };
  const openFinding = (finding: Finding, opener: HTMLElement) => {
    openerRef.current = opener;
    setOpenId(finding.id);
  };

  return (
    <WorkspaceShell>
      <div className={styles.screen}>
        <header className={styles.header}>
          <h1 ref={headingRef} tabIndex={-1} className={styles.title}>
            {t(corrected ? 'results.document_ready' : 'results.report_ready')}
          </h1>
          <p className={styles.subtitle}>
            {t(corrected ? 'results.document_subtitle' : 'results.report_subtitle')}
          </p>
        </header>
        {results.demo && (
          <p className="notice small" role="note">
            {t('results.demo_notice')}
          </p>
        )}
        <section className={styles.output} aria-labelledby="output-title">
          <div className={styles.outputTitle}>
            <FileKindIcon kind="xlsx" size={24} />
            <h2 id="output-title">{corrected && doc ? doc.title : t('results.all_findings')}</h2>
          </div>
          {corrected && doc ? (
            <>
              <p className="muted">{t('results.output_description')}</p>
              {doc.period_start && doc.period_end && (
                <p className="small muted">
                  {t('results.doc_period', {
                    start: formatShortDate(doc.period_start),
                    end: formatShortDate(doc.period_end),
                  })}
                </p>
              )}
              <ExportButtons
                createHref={backHref}
                preferred={manifest.export_format}
                corrected
                available={Boolean(onExport)}
                onExport={(format) => void exportAs(format)}
                status={exportStatus}
                busy={exportBusy}
                failed={exportFailed}
              />
              <p className="small muted">
                {t('results.output_meta', {
                  titles: doc.receivables.length,
                  entries: doc.bank_entries.length,
                })}
              </p>
            </>
          ) : (
            <>
              <p className="muted">{t('results.legacy_output')}</p>
              <ExportButtons
                createHref={backHref}
                preferred={manifest.export_format}
                available={Boolean(onExport)}
                onExport={(format) => void exportAs(format)}
                status={exportStatus}
                busy={exportBusy}
                failed={exportFailed}
              />
            </>
          )}
        </section>
        {critical.length > 0 && (
          <aside className={styles.warning} aria-labelledby="critical-title">
            <h2 id="critical-title">
              <WarningIcon size={20} />
              {t('results.critical_heading')}
            </h2>
            <ul>
              {critical.map((f) => (
                <li key={f.id}>
                  <button
                    className={styles.warningLink}
                    type="button"
                    onClick={(event) => openFinding(f, event.currentTarget)}
                    aria-label={t('results.critical_open', { title: f.title })}
                  >
                    {f.title}
                  </button>
                  <span className="small muted">{t(STATUS_LABEL[f.status])}</span>
                </li>
              ))}
            </ul>
            <p className="small muted">{t('results.critical_scope')}</p>
          </aside>
        )}
        {results.sourceWarnings && results.sourceWarnings.length > 0 && (
          <aside className={styles.warning}>
            <h2>{t('results.source_warnings_title')}</h2>
            <ul>
              {results.sourceWarnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          </aside>
        )}
        {corrected && doc && doc.receivables.length > 0 && (
          <div className={styles.balance}>
            <span>{t('results.doc_open')}</span>
            <strong>{formatCurrency(doc.totals.open_balance / 100, doc.currency)}</strong>
          </div>
        )}
        <div className={styles.sections}>
          <Disclosure
            title={t('results.review_heading')}
            description={t('results.review_count', { count: pending.length })}
          >
            {corrected && doc && <DocumentPending document={doc} />}
            <p className="small muted">{t('results.review_scope')}</p>
            {pending.length ? (
              <FindingsTable items={pending} onOpen={openFinding} />
            ) : (
              <p className="muted">{t('results.review_empty')}</p>
            )}
          </Disclosure>
          {corrected && doc && (
            <Disclosure title={t('results.changes_heading')}>
              <DocumentChanges document={doc} />
            </Disclosure>
          )}
          <Disclosure title={t('results.all_heading')} description={t('results.all_description')}>
            {corrected && doc && <ReconciledDocument document={doc} />}
            <section className={styles.section} aria-label={t('results.all_findings')}>
              <h3>{manifest.findings_label}</h3>
              <p className="small muted">
                {t(('workspace.sort_' + manifest.sort_by) as Parameters<typeof t>[0])}{' '}
                {t('results.findings_scope')}
              </p>
              {findings.length ? (
                manifest.group_by === 'none' ? (
                  <FindingsTable items={findings} onOpen={openFinding} />
                ) : (
                  [
                    ...new Set(
                      findings.map((f) =>
                        manifest.group_by === 'severity' ? (f.severity ?? 'medium') : f.matchType,
                      ),
                    ),
                  ].map((group) => (
                    <section key={group}>
                      <h4>{t(('workspace.group_value_' + group) as Parameters<typeof t>[0])}</h4>
                      <FindingsTable
                        items={findings.filter(
                          (f) =>
                            (manifest.group_by === 'severity'
                              ? (f.severity ?? 'medium')
                              : f.matchType) === group,
                        )}
                        onOpen={openFinding}
                      />
                    </section>
                  ))
                )
              ) : (
                <p className="muted">{t('results.exceptions_empty')}</p>
              )}
            </section>
            {results.cleanMatches.length > 0 && (
              <Disclosure
                title={t('results.clean_summary', { count: results.cleanMatches.length })}
              >
                <div className={styles.tableWrap}>
                  <table className={styles.table}>
                    <thead>
                      <tr>
                        <th scope="col">{t('results.col_description')}</th>
                        <th scope="col">{t('results.col_date')}</th>
                        <th scope="col">{t('results.col_value')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {results.cleanMatches.map((item) => (
                        <tr key={item.id}>
                          <td>
                            {item.description}
                            <p className="small muted">
                              {item.leftSource} ↔ {item.rightSource}
                            </p>
                          </td>
                          <td>{formatShortDate(item.date)}</td>
                          <td>{formatCurrency(item.amount, item.currency)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Disclosure>
            )}
          </Disclosure>
        </div>
        <footer className={styles.footer}>
          <p>{t('results.doc_preserved')}</p>
          <p>{t('results.doc_boundary')}</p>
          <a href={backHref}>{t('results.back_to_form')}</a>
        </footer>
        {open && (
          <FindingDrawer
            key={open.id}
            finding={open}
            onClose={closeDrawer}
            onDecision={decide}
            allowUndo={allowUndo}
            canDecide={canDecide}
          />
        )}
        <WorkspacePanel />
      </div>
    </WorkspaceShell>
  );
}
