import { t } from '../lib/i18n';
import styles from './ProcessingScene.module.css';

export type ProcessingPhase = 'upload' | 'scan' | 'estimate' | 'start' | 'reconcile';
export interface ProcessingState {
  phase: ProcessingPhase;
  message: string;
}

const steps = ['upload', 'scan', 'reconcile'] as const;

/** An indeterminate animation follows real processing stages, without inventing a percentage. */
export function ProcessingScene({ phase, message }: ProcessingState) {
  const active = phase === 'upload' ? 0 : phase === 'scan' ? 1 : 2;
  return (
    <section className={styles.scene} data-phase={phase} aria-label={t('processing.label')}>
      <div className={styles.art} aria-hidden="true">
        <div className={styles.halo} />
        <div className={`${styles.document} ${styles.left}`}>
          <span />
          <span />
          <span />
        </div>
        <div className={`${styles.document} ${styles.right}`}>
          <span />
          <span />
          <span />
        </div>
        <div className={styles.connector} />
        <div className={styles.core}>
          <svg viewBox="0 0 32 32" fill="none">
            <path
              d="m8 16 5 5 11-11"
              stroke="currentColor"
              strokeWidth="3"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </div>
        <div className={styles.scan} />
      </div>
      <div className={styles.copy}>
        <p className={styles.eyebrow}>{t('processing.label')}</p>
        <p className={styles.message} role="status" aria-live="polite" aria-atomic="true">
          {message}
        </p>
        <p className={styles.hint}>{t('processing.hint')}</p>
      </div>
      <ol className={styles.steps} aria-label={t('processing.steps')}>
        {steps.map((step, index) => (
          <li
            key={step}
            data-active={index === active ? 'true' : undefined}
            data-done={index < active ? 'true' : undefined}
            aria-current={index === active ? 'step' : undefined}
          >
            <span className={styles.dot} aria-hidden="true" />
            {t(`processing.${step}`)}
          </li>
        ))}
      </ol>
      <div className={styles.track} aria-hidden="true">
        <span />
      </div>
    </section>
  );
}
