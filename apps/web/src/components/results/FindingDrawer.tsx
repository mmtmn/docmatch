import { useEffect, useId, useRef, useState } from 'react';
import { useCharLimit } from '../../hooks/useCharLimit';
import { formatCurrency } from '../../lib/format';
import { t, type MessageKey } from '../../lib/i18n';
import { textLimits } from '../../lib/limits';
import { CharCounter } from '../CharCounter';
import { CloseIcon } from '../icons';
import { ConfidenceBadge } from './ConfidenceBadge';
import { EvidencePanel } from './EvidencePanel';
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

export interface FindingDrawerProps {
  finding: Finding;
  onClose: () => void;
  onDecision: (id: string, status: FindingStatus, note?: string) => void;
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * Side panel with the evidence side by side and the accept / reject / edit controls.
 * Modal: Esc closes, Tab cycles inside, focus returns to the opener (handled by the parent).
 * The parent renders it with `key={finding.id}` so each finding starts from fresh state.
 */
export function FindingDrawer({ finding, onClose, onDecision }: FindingDrawerProps) {
  const id = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const [editing, setEditing] = useState(false);
  const [note, setNote] = useState(finding.note ?? '');
  const [saved, setSaved] = useState<string | null>(null);
  const noteLimit = useCharLimit(note, textLimits.finding_feedback_note);

  // Move focus into the dialog on open.
  useEffect(() => {
    panelRef.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus();
  }, []);

  // Esc closes; Tab cycles inside the panel.
  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        onClose();
        return;
      }
      if (event.key !== 'Tab' || !panelRef.current) return;
      const items = Array.from(panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (items.length === 0) return;
      const first = items[0]!;
      const last = items[items.length - 1]!;
      const inside = panelRef.current.contains(document.activeElement);
      if (event.shiftKey && (document.activeElement === first || !inside)) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (document.activeElement === last || !inside)) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [onClose]);

  const decide = (status: FindingStatus, withNote?: string): void => {
    onDecision(finding.id, status, withNote);
    setSaved(t('results.feedback_saved'));
    setEditing(false);
  };

  return (
    <div className={styles.backdrop}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${id}-title`}
        className={styles.panel}
      >
        <header className={styles.header}>
          <div className={styles.headerText}>
            <p className={styles.kicker}>
              {t(MATCH_TYPE_LABEL[finding.matchType])} · {t(STATUS_LABEL[finding.status])}
            </p>
            <h2 id={`${id}-title`} className={styles.title}>
              {finding.title}
            </h2>
          </div>
          <button
            type="button"
            className={styles.close}
            aria-label={t('results.drawer_close')}
            onClick={onClose}
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

        <section className={styles.section} aria-labelledby={`${id}-explanation`}>
          <h3 id={`${id}-explanation`} className={styles.sectionTitle}>
            {t('results.explanation')}
          </h3>
          <p>{finding.explanation}</p>
          <p className="small muted">{t('results.verified_by')}</p>
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
          {editing ? (
            <form
              className={styles.editForm}
              onSubmit={(event) => {
                event.preventDefault();
                decide('edited', note.trim());
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
              ) : (
                <button type="button" className="btn btn--ghost" onClick={() => decide('open', '')}>
                  {t('results.undo')}
                </button>
              )}
            </div>
          )}
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
      </div>
    </div>
  );
}
