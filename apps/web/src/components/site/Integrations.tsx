import { useEffect, useId, useRef, useState, useSyncExternalStore } from 'react';
import catalog from '../../../../../services/api/internal/platforms/integrations.json';
import { api } from '../../lib/api';
import { trapDialogFocus } from '../../lib/dialog';
import { t } from '../../lib/i18n';
import styles from './Integrations.module.css';

const subscribe = () => () => {};
const ready = () => true;
const notReady = () => false;
type Category = 'all' | 'finance' | 'accounting' | 'legal';
type Status = 'saving' | 'saved' | 'error';
let fallbackVisitor: string | undefined;
function visitorID() {
  try {
    const previous = localStorage.getItem('docmatch.integration-visitor');
    if (
      previous &&
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(previous)
    )
      return previous;
    const value = crypto.randomUUID();
    localStorage.setItem('docmatch.integration-visitor', value);
    return value;
  } catch {
    return (fallbackVisitor ??= crypto.randomUUID());
  }
}
export function Integrations() {
  const hydrated = useSyncExternalStore(subscribe, ready, notReady);
  const dialog = useRef<HTMLDialogElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  const id = useId();
  const [open, setOpen] = useState(false);
  const [category, setCategory] = useState<Category>('all');
  const [states, setStates] = useState<Record<string, Status>>({});
  const [message, setMessage] = useState('');
  const busy = useRef(new Set<string>());
  useEffect(() => {
    if (!open) return;
    const node = dialog.current;
    const trigger = opener.current;
    node?.showModal();
    const before = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      node?.close();
      document.body.style.overflow = before;
      trigger?.focus();
    };
  }, [open]);
  async function integrate(item: (typeof catalog)[number]) {
    if (busy.current.has(item.id) || states[item.id] === 'saved') return;
    busy.current.add(item.id);
    setStates((current) => ({ ...current, [item.id]: 'saving' }));
    setMessage('');
    try {
      await api('/v1/integration-interests', {
        body: { integration: item.id, visitor_id: visitorID() },
        orgId: null,
        signal: AbortSignal.timeout(15000),
      });
      setStates((current) => ({ ...current, [item.id]: 'saved' }));
      setMessage(t('integrations.saved', { name: item.name }));
    } catch {
      setStates((current) => ({ ...current, [item.id]: 'error' }));
      setMessage(t('integrations.failed'));
    } finally {
      busy.current.delete(item.id);
    }
  }
  return (
    <>
      <button
        ref={opener}
        className={styles.trigger}
        type="button"
        disabled={!hydrated}
        aria-haspopup="dialog"
        onClick={() => setOpen(true)}
      >
        <svg
          viewBox="0 0 24 24"
          width="18"
          height="18"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
          aria-hidden="true"
        >
          <rect x="3" y="3" width="7" height="7" rx="2" />
          <rect x="14" y="3" width="7" height="7" rx="2" />
          <rect x="3" y="14" width="7" height="7" rx="2" />
          <path d="M17.5 14v7M14 17.5h7" />
        </svg>
        {t('integrations.nav')}
      </button>
      {open && (
        <dialog
          ref={dialog}
          className={styles.dialog}
          aria-labelledby={`${id}-title`}
          aria-describedby={`${id}-intro`}
          onKeyDown={trapDialogFocus}
          onCancel={(event) => {
            event.preventDefault();
            setOpen(false);
          }}
        >
          <div className={styles.heading}>
            <div>
              <p className={styles.eyebrow}>{t('integrations.eyebrow')}</p>
              <h2 id={`${id}-title`}>{t('integrations.title')}</h2>
            </div>
            <button
              type="button"
              className={styles.close}
              onClick={() => setOpen(false)}
              aria-label={t('integrations.close')}
            >
              ×
            </button>
            <p id={`${id}-intro`} className={styles.intro}>
              {t('integrations.intro')}
            </p>
          </div>
          <div className={styles.filters} aria-label={t('integrations.nav')}>
            {(['all', 'finance', 'accounting', 'legal'] as const).map((value) => (
              <button
                type="button"
                key={value}
                aria-pressed={category === value}
                onClick={() => setCategory(value)}
              >
                {t(`integrations.${value}`)}
              </button>
            ))}
          </div>
          <ul className={styles.grid}>
            {catalog
              .filter((item) => category === 'all' || item.category === category)
              .map((item) => (
                <li key={item.id} className={styles.card}>
                  <div className={styles.brand} data-tone={item.tone} aria-hidden="true">
                    {item.mark}
                  </div>
                  <div className={styles.details}>
                    <h3>{item.name}</h3>
                    <p>{item.description}</p>
                  </div>
                  <button
                    type="button"
                    className={styles.integrate}
                    data-saved={states[item.id] === 'saved'}
                    disabled={states[item.id] === 'saving' || states[item.id] === 'saved'}
                    onClick={() => void integrate(item)}
                    aria-label={`${t(states[item.id] === 'saved' ? 'integrations.soon' : 'integrations.integrate')} ${item.name}`}
                  >
                    {t(
                      states[item.id] === 'saving'
                        ? 'integrations.saving'
                        : states[item.id] === 'saved'
                          ? 'integrations.soon'
                          : 'integrations.integrate',
                    )}
                  </button>
                </li>
              ))}
          </ul>
          <div className={styles.footer}>
            <p role="status" aria-live="polite">
              {message || t('integrations.footer')}
            </p>
          </div>
        </dialog>
      )}
    </>
  );
}
