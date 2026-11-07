import { errorMessage, projectDate } from '../../lib/display';
import { useEffect, useMemo, useState } from 'react';
import { api, ApiError } from '../../lib/api';
import { accounts, type Organization } from '../../lib/accounts';
import { selectedAccount } from '../../lib/accountContext';
import { t } from '../../lib/i18n';
import { textLimits } from '../../lib/limits';
import { projectClient, type Project, type ProjectDetail } from '../../lib/projects';
import type { Me } from '../../lib/session';
import { ProjectEditor } from './ProjectEditor';
import styles from './ProjectsApp.module.css';

export function ProjectsApp() {
  const [context, setContext] = useState<{ org: Organization; me: Me } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [signedOut, setSignedOut] = useState(false);
  const [projectId] = useState(() =>
    typeof window === 'undefined'
      ? null
      : new URLSearchParams(window.location.search).get('project'),
  );
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    void accounts(signal)
      .then(async (list) => {
        const org = list.items.find((item) => item.id === selectedAccount()?.orgId);
        if (!org) throw new Error(t('projects.no_account'));
        const me = await api<Me>('/v1/me', { orgId: org.id, signal });
        if (!signal.aborted) setContext({ org, me });
      })
      .catch((cause: unknown) => {
        if (signal.aborted) return;
        if (cause instanceof ApiError && cause.status === 401) setSignedOut(true);
        else setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, []);
  if (signedOut)
    return (
      <section className={styles.empty}>
        <h1>{t('projects.title')}</h1>
        <p>{t('projects.sign_in')}</p>
        <a className="btn btn--primary" href="/login">
          {t('nav.login')}
        </a>
      </section>
    );
  if (error || !context)
    return (
      <section className={styles.empty}>
        <h1>{t('projects.title')}</h1>
        {error ? <p role="alert">{error}</p> : <p role="status">{t('mobile.loading')}</p>}
      </section>
    );
  return (
    <section className={`${styles.page} ${projectId ? styles.detailPage : ''}`}>
      <nav className={styles.navigation} aria-label={t('projects.navigation')}>
        <a href="/app" aria-current={!projectId ? 'page' : undefined}>
          {t('projects.title')}
        </a>
        {context.org.capabilities.read_results && <a href="/atividade">{t('projects.activity')}</a>}
        <a href={context.org.capabilities.manage_account ? '/equipe' : '/conta'}>
          {context.org.capabilities.manage_account
            ? t('projects.team_plan')
            : t('projects.my_profile')}
        </a>
      </nav>
      {context.org.features?.projects === false && <p role="status">{t('projects.paused')}</p>}
      {projectId ? (
        <ProjectLoader
          key={`${context.org.id}:${projectId}`}
          org={context.org}
          me={context.me}
          id={projectId}
        />
      ) : (
        <ProjectList org={context.org} />
      )}
    </section>
  );
}

function ProjectLoader({ org, me, id }: { org: Organization; me: Me; id: string }) {
  const client = useMemo(() => projectClient(org.id), [org.id]);
  const [project, setProject] = useState<ProjectDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    void client
      .get(id, controller.signal)
      .then(setProject)
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [client, id]);
  if (error) return <p role="alert">{error}</p>;
  if (!project) return <p role="status">{t('mobile.loading')}</p>;
  return <ProjectEditor initial={project} org={org} me={me} client={client} />;
}

function ProjectList({ org }: { org: Organization }) {
  const client = useMemo(() => projectClient(org.id), [org.id]);
  const [items, setItems] = useState<Project[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [name, setName] = useState('');
  const [archived, setArchived] = useState(false);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    void client
      .list(archived, undefined, controller.signal)
      .then((page) => {
        setItems(page.items);
        setCursor(page.next_cursor ?? null);
        setLoading(false);
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(cause));
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [client, archived]);
  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      const project = await client.create(name);
      window.location.assign(`/app?project=${encodeURIComponent(project.id)}`);
    } catch (cause) {
      setError(errorMessage(cause));
      setBusy(false);
    }
  };
  const more = async () => {
    if (!cursor) return;
    setBusy(true);
    try {
      const page = await client.list(archived, cursor);
      setItems((previous) => [...previous, ...page.items]);
      setCursor(page.next_cursor ?? null);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <header className={styles.heading}>
        <div>
          <h1>{t('projects.title')}</h1>
          <p>{t('projects.intro')}</p>
        </div>
        <span className={styles.muted}>
          {org.kind === 'personal' ? t('projects.personal_account') : org.name}
        </span>
      </header>
      {org.features?.projects === false && ['owner', 'admin', 'member'].includes(org.role) && (
        <a className="btn btn--primary" href={`/?account=${encodeURIComponent(org.id)}#conciliar`}>
          {t('projects.quick_run')}
        </a>
      )}
      {org.capabilities.create_project && (
        <form
          className={styles.create}
          onSubmit={(event) => {
            event.preventDefault();
            void create();
          }}
        >
          <label htmlFor="project-name">{t('projects.name')}</label>
          <div className={styles.actions}>
            <input
              id="project-name"
              value={name}
              maxLength={textLimits.project_name}
              required
              placeholder={t('projects.name_example')}
              onChange={(event) => setName(event.target.value)}
            />
            <button className="btn btn--primary" disabled={busy || !name.trim()}>
              {t('projects.new')}
            </button>
          </div>
        </form>
      )}
      <label className={styles.filter}>
        <input
          type="checkbox"
          checked={archived}
          onChange={(event) => {
            setLoading(true);
            setArchived(event.target.checked);
          }}
        />
        {t('projects.show_archived')}
      </label>
      {error && <p role="alert">{error}</p>}
      {loading ? (
        <p role="status">{t('mobile.loading')}</p>
      ) : items.length === 0 ? (
        <div className={styles.empty}>
          <h2>{t('projects.empty_title')}</h2>
          <p>
            {org.features?.projects === false
              ? t('projects.paused')
              : org.capabilities.create_project
                ? t('projects.empty_body')
                : t('projects.empty_upload_only')}
          </p>
        </div>
      ) : (
        <ul className={styles.list}>
          {items.map((project) => (
            <li key={project.id}>
              <a className={styles.card} href={`/app?project=${encodeURIComponent(project.id)}`}>
                <strong>{project.name}</strong>
                <span>{t('projects.documents_count', { count: project.document_count })}</span>
                <small>{t('projects.updated', { date: projectDate(project.updated_at) })}</small>
              </a>
            </li>
          ))}
        </ul>
      )}
      {cursor && (
        <button
          type="button"
          className="btn btn--ghost"
          disabled={busy}
          onClick={() => {
            void more();
          }}
        >
          {t('projects.more')}
        </button>
      )}
    </>
  );
}
