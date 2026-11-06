import { trapDialogFocus } from '../../lib/dialog';
import { useEffect, useId, useRef, useState } from 'react';
import { useCharLimit } from '../../hooks/useCharLimit';
import { formatCurrency } from '../../lib/format';
import { t, type MessageKey } from '../../lib/i18n';
import { textLimits } from '../../lib/limits';
import { CharCounter } from '../CharCounter';
import { CloseIcon } from '../icons';
import { ConfidenceBadge } from './ConfidenceBadge';
import { EvidencePanel } from './EvidencePanel';
import { DocumentRowEffect } from './ReconciledDocument';
import type { Finding, FindingStatus, MatchType } from './types';
import styles from './FindingDrawer.module.css';

export const MATCH_TYPE_LABEL: Record<MatchType, MessageKey> = {
  missing: 'results.match_type_missing',
  amount: 'results.match_type_amount',
  date: 'results.match_type_date',
  duplicate: 'results.match_type_duplicate',
};

export const STATUS_LABEL: Record<FindingStatus, MessageKey> = {
  open: 'results.status_open',
  accepted: 'results.status_accepted',
  rejected: 'results.status_rejected',
  edited: 'results.status_edited',
};

const PRIORITY_LABEL: Record<string, MessageKey> = {
  critical: 'results.priority_critical',
  high: 'results.priority_high',
  medium: 'results.priority_medium',
  low: 'results.priority_low',
  info: 'results.priority_info',
};

export interface FindingDrawerProps {
  finding: Finding;
  onClose: () => void;
  onDecision: (id: string, status: FindingStatus, note?: string) => Promise<void> | void;
  allowUndo?: boolean;
  canDecide?: boolean;
}

/**
 * Side panel with the evidence side by side and the accept / reject / edit controls.
 * Modal: Esc closes, Tab cycles inside, focus returns to the opener (handled by the parent).
 * The parent renders it with `key={finding.id}` so each finding starts from fresh state.
 */
export function FindingDrawer({
  finding,
  onClose,
  onDecision,
  allowUndo = true,
  canDecide = true,
}: FindingDrawerProps) {
  const id = useId();
  const panelRef = useRef<HTMLDialogElement>(null);
  const [editing, setEditing] = useState(false);
  const [note, setNote] = useState(finding.note ?? '');
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const noteLimit = useCharLimit(note, textLimits.finding_feedback_note);

  const dismiss = () => {
    // Release the native modal's inert background before the parent restores focus.
    panelRef.current?.close?.();
    onClose();
  };

  useEffect(() => {
    const panel = panelRef.current;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    if (panel && !panel.open) {
      if (typeof panel.showModal === 'function') panel.showModal();
      else panel.setAttribute('open', '');
    }
    return () => {
      panel?.close?.();
      document.body.style.overflow = previousOverflow;
    };
  }, []);

  const decide = async (status: FindingStatus, withNote?: string): Promise<void> => {
    if (busy || !canDecide) return;
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      await onDecision(finding.id, status, withNote);
      setSaved(t('results.feedback_saved'));
      setEditing(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('errors.unknown'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <dialog
      onKeyDown={trapDialogFocus}
      ref={panelRef}
      aria-modal="true"
      aria-labelledby={`${id}-title`}
      className={styles.panel}
      onCancel={(event) => {
        event.preventDefault();
        dismiss();
      }}
    >
      <header className={styles.header}>
        <div className={styles.headerText}>
          <p className={styles.kicker}>
            {finding.severity && PRIORITY_LABEL[finding.severity]
              ? `${t('results.col_priority')}: ${t(PRIORITY_LABEL[finding.severity]!)}`
              : t(MATCH_TYPE_LABEL[finding.matchType])}{' '}
            · {t(STATUS_LABEL[finding.status])}
          </p>
          <h2 id={`${id}-title`} className={styles.title}>
            {finding.title}
          </h2>
        </div>
        <button
          type="button"
          className={styles.close}
          aria-label={t('results.drawer_close')}
          onClick={dismiss}
        >
          <CloseIcon size={20} />
        </button>
      </header>

      <dl className={styles.facts}>
        <div>
          <dt>{t('results.amount_at_risk')}</dt>
          <dd className={styles.amount}>
            {formatCurrency(finding.amountAtRisk, finding.currency)}
          </dd>
        </div>
        <div>
          <dt>{t('results.col_confidence')}</dt>
          <dd>
            <ConfidenceBadge level={finding.confidence} />
          </dd>
        </div>
        {finding.rule && (
          <div>
            <dt>{t('results.rule_applied')}</dt>
            <dd>{finding.rule}</dd>
          </div>
        )}
      </dl>

      <section className={styles.section} aria-labelledby={`${id}-file-effect`}>
        <h3 id={`${id}-file-effect`} className={styles.sectionTitle}>
          {t('results.file_effect')}
        </h3>
        {finding.documentRows?.length ? (
          finding.documentRows.map((row) => (
            <DocumentRowEffect
              key={`${row.source_id}:${row.id}`}
              row={row}
              currency={finding.currency}
            />
          ))
        ) : (
          <p className="small muted">{t('results.file_effect_unknown')}</p>
        )}
      </section>
      <section className={styles.section} aria-labelledby={`${id}-explanation`}>
        <h3 id={`${id}-explanation`} className={styles.sectionTitle}>
          {t('results.explanation')}
        </h3>
        <p>{finding.explanation}</p>
        <p className="small muted">{t('results.confidence_scope')}</p>
      </section>

      <section className={styles.section} aria-labelledby={`${id}-evidence`}>
        <h3 id={`${id}-evidence`} className={styles.sectionTitle}>
          {t('results.evidence_heading')}
        </h3>
        <div className={styles.sideBySide} aria-label={t('results.evidence_side_by_side')}>
          <EvidencePanel evidence={finding.left} currency={finding.currency} />
          <EvidencePanel evidence={finding.right} currency={finding.currency} />
        </div>
      </section>

      <footer className={styles.footer}>
        <p className={styles.reviewScope}>{t('results.review_scope')}</p>
        <fieldset
          disabled={busy || !canDecide}
          hidden={!canDecide}
          className={styles.decisionFields}
        >
          {editing ? (
            <form
              className={styles.editForm}
              onSubmit={(event) => {
                event.preventDefault();
                void decide('edited', note.trim());
              }}
            >
              <label className="field__label" htmlFor={`${id}-note`}>
                {t('results.edit_label')}
              </label>
              <textarea
                id={`${id}-note`}
                className="textarea"
                rows={3}
                maxLength={textLimits.finding_feedback_note}
                placeholder={t('results.edit_placeholder')}
                value={note}
                aria-describedby={`${id}-note-counter`}
                onChange={(event) => setNote(event.target.value)}
              />
              <CharCounter id={`${id}-note-counter`} state={noteLimit} />
              <div className={styles.actions}>
                <button type="submit" className="btn btn--primary">
                  {t('results.edit_save')}
                </button>
                <button type="button" className="btn btn--ghost" onClick={() => setEditing(false)}>
                  {t('results.edit_cancel')}
                </button>
              </div>
            </form>
          ) : (
            <div className={styles.actions}>
              {finding.status === 'open' ? (
                <>
                  <button
                    type="button"
                    className="btn btn--primary"
                    onClick={() => decide('accepted')}
                  >
                    {t('results.accept')}
                  </button>
                  <button type="button" className="btn" onClick={() => decide('rejected')}>
                    {t('results.reject')}
                  </button>
                  <button type="button" className="btn btn--ghost" onClick={() => setEditing(true)}>
                    {t('results.edit')}
                  </button>
                </>
              ) : allowUndo ? (
                <button type="button" className="btn btn--ghost" onClick={() => decide('open', '')}>
                  {t('results.undo')}
                </button>
              ) : (
                <button type="button" className="btn btn--ghost" onClick={() => setEditing(true)}>
                  {t('results.edit')}
                </button>
              )}
            </div>
          )}
        </fieldset>
        {error && <p role="alert">{error}</p>}
        <p className={styles.saved} role="status" aria-live="polite">
          {saved ?? ''}
        </p>
        {finding.note && !editing && (
          <p className={styles.note}>
            <span className="muted">{t('results.edit_label')}: </span>
            {finding.note}
          </p>
        )}
      </footer>
    </dialog>
  );
}
