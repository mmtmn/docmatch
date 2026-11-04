import type { CharLimitState } from '../hooks/useCharLimit';
import styles from './CharCounter.module.css';

export interface CharCounterProps {
  id: string;
  state: CharLimitState;
}

/** Live counter shown from 80% of the limit; turns into the limit message at 100%. */
export function CharCounter({ id, state }: CharCounterProps) {
  return (
    <p
      id={id}
      className={styles.counter}
      data-at-limit={state.atLimit ? 'true' : undefined}
      aria-live="polite"
    >
      {state.showCounter ? state.message : ''}
    </p>
  );
}
