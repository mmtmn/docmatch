import { useId, type MouseEvent } from 'react';
import { t } from '../lib/i18n';
import styles from './RunButton.module.css';

export interface Quote {
  /** Expected tokens for the run. */
  expected: number;
  /** Cap: the run never charges above this. */
  cap: number;
}

export interface RunButtonProps {
  disabled: boolean;
  /** Why the button is disabled; announced via aria-describedby. */
  reason?: string | null;
  quote?: Quote | null;
  quoteLoading?: boolean;
  busy?: boolean;
}

/**
 * Submit button for the hero form. Uses aria-disabled instead of `disabled` so keyboard and
 * screen-reader users can still reach it and hear the reason it is inactive.
 */
export function RunButton({
  disabled,
  reason = null,
  quote = null,
  quoteLoading = false,
  busy = false,
}: RunButtonProps) {
  const descriptionId = useId();
  const inactive = disabled || busy;
  const status = busy
    ? t('form.disabled_submitting')
    : disabled
      ? (reason ?? '')
      : quote
        ? t('form.estimate', { expected: quote.expected, cap: quote.cap })
        : quoteLoading
          ? t('form.estimate_loading')
          : '';

  const onClick = (event: MouseEvent<HTMLButtonElement>): void => {
    if (inactive) event.preventDefault();
  };

  return (
    <div className={styles.wrap}>
      <button
        type="submit"
        className={`btn btn--primary ${styles.button}`}
        aria-disabled={inactive ? 'true' : undefined}
        aria-describedby={descriptionId}
        data-busy={busy ? 'true' : undefined}
        onClick={onClick}
      >
        {busy ? t('form.submitting') : t('form.submit')}
      </button>
      <p
        id={descriptionId}
        className={styles.status}
        aria-live="polite"
        data-kind={quote && !inactive ? 'estimate' : 'reason'}
      >
        {status}
      </p>
    </div>
  );
}
