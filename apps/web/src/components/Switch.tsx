import type { ReactNode } from 'react';
import styles from './Switch.module.css';

export interface SwitchProps {
  id?: string;
  label: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  /** Rendered after the control (an InfoTip, typically). */
  children?: ReactNode;
}

/** A real `role="switch"` with its visible label inside the 44px target. */
export function Switch({ id, label, checked, onChange, disabled = false, children }: SwitchProps) {
  return (
    <span className={styles.wrap}>
      <button
        type="button"
        role="switch"
        id={id}
        aria-checked={checked}
        disabled={disabled}
        className={styles.switch}
        onClick={() => onChange(!checked)}
      >
        <span className={styles.track} aria-hidden="true">
          <span className={styles.thumb} />
        </span>
        <span className={styles.label}>{label}</span>
      </button>
      {children}
    </span>
  );
}
