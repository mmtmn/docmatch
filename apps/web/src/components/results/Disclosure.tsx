import { useState, type ReactNode } from 'react';
import { ChevronIcon } from '../icons';
import styles from './ResultsScreen.module.css';

/** Keep dense content out of the initial view and accessibility tree. */
export function Disclosure({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  return (
    <details className={styles.disclosure} onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>
        <span>
          <span className={styles.disclosureTitle}>{title}</span>
          {description && <span className={styles.disclosureDescription}>{description}</span>}
        </span>
        <ChevronIcon size={20} />
      </summary>
      {open && <div className={styles.disclosureBody}>{children}</div>}
    </details>
  );
}
