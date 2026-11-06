import type { ReconciliationDocument, ReconciledReceivable } from '../../lib/pipeline';
import { formatCurrency, formatShortDate } from '../../lib/format';
import { t } from '../../lib/i18n';
import { Disclosure } from './Disclosure';
import styles from './ReconciledDocument.module.css';

export function documentStatus(row: ReconciledReceivable): string {
  switch (row.status) {
    case 'paid':
      return t('results.doc_paid');
    case 'partial':
      return t('results.doc_partial');
    case 'duplicate':
      return t('results.doc_duplicate_status', { id: row.duplicate_of });
    case 'review':
      return t('results.doc_review');
    case 'withholding':
      return t('results.doc_withholding');
    default:
      return t('results.doc_unpaid');
  }
}

/** Explain the actual snapshot, never infer an applied adjustment from a review verdict. */
export function DocumentRowEffect({
  row,
  currency,
}: {
  row: ReconciledReceivable;
  currency: string;
}) {
  const money = (cents: number) => formatCurrency(cents / 100, currency);
  const pending = row.proposed_amount > 0 || row.status === 'withholding';
  const applied = row.applied_principal > 0 || row.status === 'duplicate';
  return (
    <div className={styles.effect}>
      <p className={styles.effectTitle}>
        {row.customer} · {row.reference || row.id}
      </p>
      <p className={styles.effectState}>
        {t(
          pending
            ? applied
              ? 'results.file_partial_pending'
              : 'results.file_pending'
            : applied
              ? 'results.file_applied'
              : 'results.file_unchanged',
        )}
      </p>
      <p>
        {row.status === 'duplicate'
          ? t('results.file_duplicate_change', {
              amount: money(row.original_amount),
              id: row.duplicate_of,
            })
          : t('results.file_balance_change', {
              before: money(row.original_amount),
              after: money(row.open_amount),
            })}
      </p>
      {row.proposed_amount > 0 && (
        <p>
          {t('results.file_proposed_change', {
            amount: money(row.proposed_amount),
            after: money(Math.max(0, row.open_amount - row.proposed_amount)),
          })}
        </p>
      )}
      {row.status === 'withholding' && (
        <p>{t('results.doc_tax_note', { amount: money(row.open_amount) })}</p>
      )}
      <p className="small muted">
        {t('results.doc_source', { name: row.source_name, row: row.source_row })}
      </p>
    </div>
  );
}

export function DocumentPending({ document: doc }: { document: ReconciliationDocument }) {
  const rows = doc.receivables.filter(
    (row) => row.proposed_amount > 0 || row.status === 'withholding',
  );
  if (!rows.length && doc.totals.proposed_amount === 0 && doc.totals.withholding_pending === 0)
    return null;
  return (
    <section className={styles.effects} aria-label={t('results.document_pending_heading')}>
      <h3>{t('results.document_pending_heading')}</h3>
      <p className="small muted">{t('results.file_pending_help')}</p>
      {rows.map((row) => (
        <DocumentRowEffect key={`${row.source_id}:${row.id}`} row={row} currency={doc.currency} />
      ))}
    </section>
  );
}

export function DocumentChanges({ document: doc }: { document: ReconciliationDocument }) {
  const money = (cents: number) => formatCurrency(cents / 100, doc.currency);
  const rows = doc.receivables.filter(
    (row) => row.applied_principal > 0 || row.status === 'duplicate',
  );
  return (
    <section className={styles.effects} aria-label={t('results.changes_heading')}>
      <ul className={styles.changeList}>
        {doc.totals.principal_confirmed > 0 && (
          <li>{t('results.changes_paid', { amount: money(doc.totals.principal_confirmed) })}</li>
        )}
        {doc.totals.duplicate_excluded > 0 && (
          <li>
            {t('results.changes_duplicates', { amount: money(doc.totals.duplicate_excluded) })}
          </li>
        )}
        {doc.totals.proposed_amount > 0 && (
          <li>{t('results.changes_pending', { amount: money(doc.totals.proposed_amount) })}</li>
        )}
      </ul>
      {rows.length ? (
        <Disclosure title={t('results.changes_details')}>
          {rows.map((row) => (
            <DocumentRowEffect
              key={`${row.source_id}:${row.id}`}
              row={row}
              currency={doc.currency}
            />
          ))}
        </Disclosure>
      ) : (
        <p>{t('results.changes_none')}</p>
      )}
    </section>
  );
}

/** Full source-backed data, opened only on demand by the results screen. */
export function ReconciledDocument({ document: doc }: { document: ReconciliationDocument }) {
  const money = (cents: number) => formatCurrency(cents / 100, doc.currency);
  return (
    <div className={styles.document}>
      {doc.receivables.length > 0 && (
        <section aria-label={t('results.doc_ledger')}>
          <div className={styles.tableHeading}>
            <h3>{t('results.doc_ledger')}</h3>
            <p>{t('results.doc_count', { count: doc.receivables.length })}</p>
          </div>
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th scope="col">{t('results.doc_customer')}</th>
                  <th scope="col">{t('results.doc_original')}</th>
                  <th scope="col">{t('results.doc_received')}</th>
                  <th scope="col">{t('results.doc_balance')}</th>
                  <th scope="col">{t('results.doc_status')}</th>
                </tr>
              </thead>
              <tbody>
                {doc.receivables.map((row) => (
                  <tr key={`${row.source_id}:${row.id}`}>
                    <td>
                      <strong>{row.customer}</strong>
                      <span>
                        {row.reference} · {row.id}
                      </span>
                      <small>
                        {t('results.doc_source', { name: row.source_name, row: row.source_row })}
                      </small>
                    </td>
                    <td data-label={t('results.doc_original')}>{money(row.original_amount)}</td>
                    <td data-label={t('results.doc_received')}>{money(row.received_amount)}</td>
                    <td className={styles.balance} data-label={t('results.doc_balance')}>
                      {money(row.open_amount)}
                    </td>
                    <td>
                      <span className={styles.badge}>{documentStatus(row)}</span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
      {doc.bank_entries.length > 0 && (
        <section aria-label={t('results.doc_bank')}>
          <div className={styles.tableHeading}>
            <h3>{t('results.doc_bank')}</h3>
            <p>{t('results.doc_bank_count', { count: doc.bank_entries.length })}</p>
          </div>
          <div className={styles.tableWrap}>
            <table className={`${styles.table} ${styles.bankTable}`}>
              <thead>
                <tr>
                  <th scope="col">{t('results.col_description')}</th>
                  <th scope="col">{t('results.col_date')}</th>
                  <th scope="col">{t('results.col_value')}</th>
                  <th scope="col">{t('results.bank_match')}</th>
                </tr>
              </thead>
              <tbody>
                {doc.bank_entries.map((row, index) => (
                  <tr key={`${row.row}:${index}`}>
                    <td>{row.description}</td>
                    <td data-label={t('results.col_date')}>{formatShortDate(row.date)}</td>
                    <td data-label={t('results.col_value')}>{money(row.amount)}</td>
                    <td>
                      <span>
                        {row.proposed
                          ? t('results.bank_proposed')
                          : row.matched_ids.length
                            ? t('results.bank_confirmed')
                            : t('results.bank_unmatched')}
                      </span>
                      <small>{row.matched_ids.join(', ')}</small>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
}
