import { useEffect, useState } from 'react';
import { accounts, type Accounts } from '../../lib/accounts';
import { selectedAccount } from '../../lib/accountContext';
import { SESSION_CHANGED_EVENT } from '../../lib/session';
import { t } from '../../lib/i18n';
import styles from './AccountSwitcher.module.css';
import { pendingInvitations } from '../../lib/teams';

/** A native select works with keyboards and opens the system sheet on phones. */
export function AccountSwitcher() {
  const [list, setList] = useState<Accounts | null>(null);
  const [orgId, setOrgId] = useState('');
  const [pending, setPending] = useState('');
  useEffect(() => {
    let controller = new AbortController();
    const load = () => {
      controller.abort();
      controller = new AbortController();
      const { signal } = controller;
      setPending('');
      accounts(signal)
        .then((next) => {
          if (signal.aborted) return;
          setList(next);
          setOrgId(selectedAccount()?.orgId ?? '');
          void pendingInvitations(undefined, signal)
            .then((inbox) => {
              if (!signal.aborted && inbox.items.length)
                setPending(`${inbox.items.length}${inbox.next_cursor ? '+' : ''}`);
            })
            .catch(() => {
              /* Account selection remains available if the inbox is offline. */
            });
        })
        .catch(() => {
          if (!signal.aborted) setList(null);
        });
    };
    load();
    window.addEventListener(SESSION_CHANGED_EVENT, load);
    return () => {
      controller.abort();
      window.removeEventListener(SESSION_CHANGED_EVENT, load);
    };
  }, []);
  if (!list?.items.length) return null;
  return (
    <div className={styles.switcher}>
      <span className="visually-hidden">{t('projects.choose_account')}</span>
      <select
        aria-label={t('projects.choose_account')}
        value={orgId}
        onChange={(event) => {
          // Commit selection on the destination page, after any unsaved-work prompt.
          const destination = window.location.pathname === '/painel' ? '/painel' : '/app';
          window.location.assign(
            `${destination}?account=${encodeURIComponent(event.currentTarget.value)}`,
          );
        }}
      >
        {list.items.map((item) => (
          <option key={item.id} value={item.id}>
            {item.kind === 'personal' ? t('projects.personal_account') : item.name}
            {item.role === 'uploader' ? ` · ${t('projects.upload_only')}` : ''}
          </option>
        ))}
      </select>
      {pending && <a href="/convites">{t('teams.inbox_link', { count: pending })}</a>}
    </div>
  );
}
