import { t, type MessageKey } from '../../lib/i18n';
import { AlertIcon, CheckIcon, TildeIcon } from '../icons';
import type { Confidence } from './types';
import styles from './ConfidenceBadge.module.css';

const LABEL: Record<Confidence, MessageKey> = {
  high: 'results.confidence_high',
  medium: 'results.confidence_medium',
  low: 'results.confidence_low',
};

/** Confidence as word + icon + colour, so no single channel carries the meaning. */
export function ConfidenceBadge({ level }: { level: Confidence }) {
  const label = t(LABEL[level]);
  const Icon = level === 'high' ? CheckIcon : level === 'medium' ? TildeIcon : AlertIcon;
  return (
    <span
      className={styles.badge}
      data-level={level}
      aria-label={t('results.confidence_label', { level: label })}
    >
      <Icon size={16} />
      <span>{label}</span>
    </span>
  );
}

export function confidenceLabel(level: Confidence): string {
  return t(LABEL[level]);
}
