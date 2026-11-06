import { useEffect, useState } from 'react';
import { accounts, type Organization } from '../../lib/accounts';
import { selectedAccount } from '../../lib/accountContext';
import { api } from '../../lib/api';
import { t, type MessageKey } from '../../lib/i18n';
import type { RunView } from '../../lib/pipeline';
import { projectResultUrl, type Page } from '../../lib/projects';
import { errorMessage, projectDate } from '../../lib/display';
import styles from './ProjectsApp.module.css';

const labels: Record<RunView['status'], MessageKey> = {
  queued: 'projects.run_queued',
  running: 'projects.run_running',
  needs_approval: 'projects.run_approval',
  completed: 'projects.run_completed',
  failed: 'projects.run_failed',
  cancelled: 'projects.run_cancelled',
};
export function ActivityApp() {
  const [org, setOrg] = useState<Organization | null>(null);
  const [items, setItems] = useState<RunView[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    void accounts(signal)
      .then(async (list) => {
        const account = list.items.find((item) => item.id === selectedAccount()?.orgId);
        if (!account) throw new Error(t('projects.no_account'));
        if (!account.capabilities.read_results) throw new Error(t('projects.results_unavailable'));
        const page = await api<Page<RunView>>('/v1/projects/activity', {
          orgId: account.id,
          signal,
        });
        if (!signal.aborted) {
          setOrg(account);
          setItems(page.items);
          setCursor(page.next_cursor ?? null);
        }
      })
      .catch((cause: unknown) => {
        if (!signal.aborted) setError(errorMessage(cause));
      })
      .finally(() => {
        if (!signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, []);
  const more = async () => {
    if (!org || !cursor || busy) return;
    setBusy(true);
    setError(null);
    try {
      const page = await api<Page<RunView>>(
        `/v1/projects/activity?cursor=${encodeURIComponent(cursor)}`,
        { orgId: org.id },
      );
      setItems((previous) => [...previous, ...page.items]);
      setCursor(page.next_cursor ?? null);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className={styles.page}>
      <nav className={styles.navigation} aria-label={t('projects.navigation')}>
        <a href="/app">{t('projects.title')}</a>
        <a href="/atividade" aria-current="page">
          {t('projects.activity')}
        </a>
        <a href="/conta">{t('projects.my_profile')}</a>
      </nav>
      <header className={styles.heading}>
        <div>
          <h1>{t('projects.activity')}</h1>
          <p>{t('projects.activity_intro')}</p>
        </div>
        {org && (
          <span className={styles.muted}>
            {org.kind === 'personal' ? t('projects.personal_account') : org.name}
          </span>
        )}
      </header>
      {error && <p role="alert">{error}</p>}
      {!busy && !error && items.length === 0 && (
        <p className={styles.empty}>{t('projects.activity_empty')}</p>
      )}
      {org && (
        <ul className={styles.list}>
          {items.map((run) => (
            <li key={run.id}>
              <a
                className={styles.card}
                href={
                  run.project_id
                    ? projectResultUrl(org.id, run.project_id, run.id)
                    : `/resultados?${new URLSearchParams({ account: org.id, run: run.id })}`
                }
              >
                <strong>{t(labels[run.status])}</strong>
                {run.created_at && <span>{projectDate(run.created_at)}</span>}
                <small>
                  {run.sources.map((file) => file.file_name).join(', ') || t('projects.legacy_run')}
                </small>
                {run.expires_at && (
                  <small>
                    {t('projects.result_expires', { date: projectDate(run.expires_at) })}
                  </small>
                )}
              </a>
            </li>
          ))}
        </ul>
      )}
      {busy && <p role="status">{t('mobile.loading')}</p>}
      {cursor && (
        <button
          className="btn btn--ghost"
          type="button"
          disabled={busy}
          onClick={() => {
            void more();
          }}
        >
          {t('projects.more')}
        </button>
      )}
    </section>
  );
}
