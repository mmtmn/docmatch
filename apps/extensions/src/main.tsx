import { useCallback, useEffect, useMemo, useState } from 'react';
import { createRoot } from 'react-dom/client';
import type { Accounts, Organization } from '../../web/src/lib/accounts';
import type { Me } from '../../web/src/lib/session';
import { ApiError } from '../../web/src/lib/apiTypes';
import { projectClient, type Project, type ProjectDetail } from '../../web/src/lib/projects';
import { uploadFile } from '../../web/src/lib/pipeline';
import { ProjectEditor } from '../../web/src/components/projects/ProjectEditor';
import { textLimits } from '../../web/src/lib/limits';
import { ProjectResults } from './results';
import { browser } from './browser';
import { config } from './config';
import { boundRequest, message } from './client';
import { t } from './i18n';
import '../../web/src/styles/global.css';
import './style.css';

export function workspaceURL(orgId?: string, projectId?: string, runId?: string) {
  const url = new URL(browser.runtime.getURL('workspace.html'));
  if (orgId) url.searchParams.set('account', orgId);
  if (projectId) url.searchParams.set('project', projectId);
  if (runId) url.searchParams.set('run', runId);
  return url.href;
}
const popup = window.location.pathname.endsWith('/popup.html');
const query = new URLSearchParams(window.location.search);
const explain = (cause: unknown) => (cause instanceof Error ? cause.message : t('errors.unknown'));
function useForeground() {
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const foreground = () => {
      if (!document.hidden) setRevision((v) => v + 1);
    };
    window.addEventListener('focus', foreground);
    document.addEventListener('visibilitychange', foreground);
    return () => {
      window.removeEventListener('focus', foreground);
      document.removeEventListener('visibilitychange', foreground);
    };
  }, []);
  return revision;
}
function ProjectPage({
  org,
  epoch,
  projectId,
  runId,
}: {
  org: Organization;
  epoch: string;
  projectId: string;
  runId: string | null;
}) {
  const [project, setProject] = useState<ProjectDetail | null>(null);
  const [me, setMe] = useState<Me | null>(null);
  const [error, setError] = useState('');
  const denied = useCallback((cause: ApiError) => {
    setProject(null);
    setMe(null);
    setError(cause.message);
  }, []);
  const request = useMemo(() => boundRequest(epoch, org.id, denied), [epoch, org.id, denied]);
  const client = useMemo(() => projectClient(org.id, request), [org.id, request]);
  const foreground = useForeground();
  useEffect(() => {
    const abort = new AbortController();
    void Promise.all([
      client.get(projectId, abort.signal),
      request<Me>('/v1/me', { orgId: org.id, signal: abort.signal }),
    ])
      .then(([p, profile]) => {
        if (abort.signal.aborted) return;
        setError('');
        setMe(profile);
        // Keep unsaved text on ordinary foreground checks; a role change replaces the view.
        setProject((previous) =>
          previous && JSON.stringify(previous.capabilities) === JSON.stringify(p.capabilities)
            ? previous
            : p,
        );
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setError(explain(cause));
      });
    return () => abort.abort();
  }, [client, request, projectId, org.id, foreground]);
  if (error && (!project || !me))
    return (
      <div role="alert">
        <p>{error}</p>
        <a href={workspaceURL(org.id)}>{t('projects.back')}</a>
      </div>
    );
  if (!project || !me) return <p role="status">{t('mobile.loading')}</p>;
  if (runId)
    return project.capabilities.read_results ? (
      <ProjectResults
        key={runId}
        runId={runId}
        project={project}
        org={org}
        me={me}
        request={request}
        backHref={workspaceURL(org.id, project.id)}
      />
    ) : (
      <p role="alert">{t('projects.results_unavailable')}</p>
    );
  return (
    <ProjectEditor
      key={JSON.stringify(project.capabilities)}
      initial={project}
      org={org}
      me={me}
      client={client}
      manageSharing={false}
      links={{ list: workspaceURL(org.id), result: workspaceURL }}
      upload={(file, scanning, signal, destination) =>
        uploadFile(file, scanning, signal, destination, {
          request,
          uploadOrigins: config.uploadOrigins,
        })
      }
    />
  );
}
function ProjectList({ org, epoch }: { org: Organization; epoch: string }) {
  const client = useMemo(() => projectClient(org.id, boundRequest(epoch, org.id)), [epoch, org.id]);
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [archived, setArchived] = useState(false);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    const abort = new AbortController();
    setProjects(null);
    setCursor(null);
    setError('');
    void client
      .list(archived, undefined, abort.signal)
      .then((page) => {
        if (!abort.signal.aborted) {
          setProjects(page.items);
          setCursor(page.next_cursor ?? null);
        }
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setError(explain(cause));
      });
    return () => abort.abort();
  }, [client, archived]);
  const create = async () => {
    if (busy || !name.trim()) return;
    setBusy(true);
    setError('');
    try {
      const project = await client.create(name.trim());
      window.location.assign(workspaceURL(org.id, project.id));
    } catch (cause) {
      setError(explain(cause));
      setBusy(false);
    }
  };
  const more = async () => {
    if (!cursor || busy) return;
    setBusy(true);
    try {
      const page = await client.list(archived, cursor);
      setProjects((previous) => [...(previous ?? []), ...page.items]);
      setCursor(page.next_cursor ?? null);
    } catch (cause) {
      setError(explain(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="extension-projects">
      <h1>{t('extensions.projects')}</h1>
      {error && <p role="alert">{error}</p>}
      {!popup && org.capabilities.create_project && (
        <form
          className="extension-create"
          onSubmit={(event) => {
            event.preventDefault();
            void create();
          }}
        >
          <label htmlFor="project-name">{t('projects.name')}</label>
          <div>
            <input
              id="project-name"
              className="input"
              value={name}
              maxLength={textLimits.project_name}
              disabled={busy}
              onChange={(e) => setName(e.target.value)}
            />
            <button className="btn btn--primary" disabled={busy || !name.trim()}>
              {t('projects.new')}
            </button>
          </div>
        </form>
      )}
      {!popup && (
        <label className="extension-archive">
          <input
            type="checkbox"
            checked={archived}
            disabled={busy}
            onChange={(event) => setArchived(event.target.checked)}
          />
          {t('projects.show_archived')}
        </label>
      )}
      {projects === null ? (
        <p role="status">{t('mobile.loading')}</p>
      ) : !projects.length ? (
        <p>{t('extensions.empty')}</p>
      ) : (
        <ul className="extension-list">
          {(popup ? projects.slice(0, 5) : projects).map((project) => (
            <li key={project.id}>
              <a
                href={workspaceURL(org.id, project.id)}
                onClick={(event) => {
                  if (popup) {
                    event.preventDefault();
                    void browser.tabs.create({ url: workspaceURL(org.id, project.id) });
                  }
                }}
              >
                <strong>{project.name}</strong>
                <span>{t('extensions.document_count', { count: project.document_count })}</span>
              </a>
            </li>
          ))}
        </ul>
      )}
      {!popup && cursor && (
        <button className="btn btn--ghost" disabled={busy} onClick={() => void more()}>
          {t('projects.more')}
        </button>
      )}
      {popup && (
        <button
          className="btn btn--primary"
          onClick={() => void browser.tabs.create({ url: workspaceURL(org.id) })}
        >
          {t('extensions.open_projects')}
        </button>
      )}
    </section>
  );
}
function SignedIn({ epoch, revision }: { epoch: string; revision: number }) {
  const [accounts, setAccounts] = useState<Organization[] | null>(null);
  const [selected, setSelected] = useState('');
  const [error, setError] = useState('');
  const request = useMemo(() => boundRequest(epoch), [epoch]);
  useEffect(() => {
    const abort = new AbortController();
    void request<Accounts>('/v1/me/organizations', { signal: abort.signal })
      .then((list) => {
        if (abort.signal.aborted) return;
        const requested = query.get('account');
        let remembered: string | null = null;
        try {
          remembered = localStorage.getItem(`docmatch.account.${epoch}`);
        } catch {
          /* Storage can be unavailable in a private session. */
        }
        const target = requested ?? remembered;
        const match = list.items.find((a) => a.id === target);
        if (requested && !match) {
          setAccounts([]);
          setSelected('');
          setError(t('projects.results_unavailable'));
          return;
        }
        setAccounts(list.items);
        setSelected((previous) =>
          list.items.some((a) => a.id === previous)
            ? previous
            : (match?.id ?? list.items[0]?.id ?? ''),
        );
        setError('');
      })
      .catch((cause) => {
        if (!abort.signal.aborted) {
          setAccounts(null);
          setSelected('');
          setError(explain(cause));
        }
      });
    return () => abort.abort();
  }, [request, revision, epoch]);
  const org = accounts?.find((account) => account.id === selected);
  const projectId = query.get('project');
  const runId = query.get('run');
  return (
    <>
      {error && <p role="alert">{error}</p>}
      {accounts === null && !error && <p role="status">{t('mobile.loading')}</p>}
      {org && (
        <>
          <label className="extension-account">
            {t('extensions.account')}
            <select
              aria-label={t('extensions.account')}
              value={selected}
              onChange={(event) => {
                const value = event.target.value;
                try {
                  localStorage.setItem(`docmatch.account.${epoch}`, value);
                } catch {
                  /* No persistence required for the request context. */
                }
                if (popup) setSelected(value);
                else window.location.assign(workspaceURL(value));
              }}
            >
              {accounts?.map((account) => (
                <option key={account.id} value={account.id}>
                  {account.kind === 'personal' ? t('projects.personal_account') : account.name}
                </option>
              ))}
            </select>
          </label>
          {!popup && projectId ? (
            <ProjectPage
              key={`${epoch}:${org.id}:${projectId}`}
              epoch={epoch}
              org={org}
              projectId={projectId}
              runId={runId}
            />
          ) : (
            <ProjectList key={`${epoch}:${org.id}`} org={org} epoch={epoch} />
          )}
        </>
      )}
    </>
  );
}
function App() {
  const [status, setStatus] = useState<{
    signedIn: boolean;
    epoch: string;
    openURL?: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [revision, setRevision] = useState(0);
  const foreground = useForeground();
  useEffect(() => {
    let active = true;
    void message<{ signedIn: boolean; epoch: string; openURL?: string }>('status')
      .then((value) => {
        if (active) {
          setStatus(value);
          setError('');
        }
      })
      .catch(() => {
        if (active) {
          setStatus((previous) =>
            previous ? { signedIn: previous.signedIn, epoch: previous.epoch } : previous,
          );
          setError(t('extensions.open_failed'));
        }
      });
    return () => {
      active = false;
    };
  }, [revision, foreground]);
  useEffect(() => {
    if (!status?.openURL) return;
    const timer = setInterval(() => setRevision((value) => value + 1), 2000);
    return () => clearInterval(timer);
  }, [status?.openURL]);
  const connect = async () => {
    setBusy(true);
    setError('');
    try {
      if (
        browser.permissions &&
        !(await browser.permissions.request({ origins: config.hostPermissions }))
      )
        throw new Error(t('extensions.permission_needed'));
      await message('signIn');
      setRevision((v) => v + 1);
    } catch (cause) {
      setError(config.browser === 'safari' ? t('extensions.safari_sign_in') : explain(cause));
    } finally {
      setBusy(false);
    }
  };
  const leave = async () => {
    setBusy(true);
    try {
      await message('signOut');
      setStatus(null);
      setRevision((v) => v + 1);
      for (const key of Object.keys(localStorage))
        if (key.startsWith('docmatch.account.')) localStorage.removeItem(key);
    } catch (cause) {
      setError(explain(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className={popup ? 'extension-popup' : 'extension-workspace'}>
      <header className="extension-header">
        <strong>docmatch</strong>
        {config.local && <small>{t('extensions.local')}</small>}
        {status?.signedIn && (
          <button className="btn btn--ghost" disabled={busy} onClick={() => void leave()}>
            {t('extensions.sign_out')}
          </button>
        )}
      </header>
      {error && <p role="alert">{error}</p>}
      {status === null ? (
        <p role="status">{t('mobile.loading')}</p>
      ) : status.signedIn ? (
        <SignedIn key={status.epoch} epoch={status.epoch} revision={foreground} />
      ) : (
        <section className="extension-connect">
          <h1>{t('extensions.projects')}</h1>
          <p>{t('extensions.connect_intro')}</p>
          {status.openURL ? (
            <>
              <a className="btn btn--primary" href={status.openURL}>
                {t('extensions.continue_app')}
              </a>
              <p>{t('extensions.return_after_login')}</p>
              <button className="btn btn--ghost" disabled={busy} onClick={() => void leave()}>
                {t('common.cancel')}
              </button>
            </>
          ) : (
            <button className="btn btn--primary" disabled={busy} onClick={() => void connect()}>
              {busy ? t('extensions.connecting') : t('nav.login')}
            </button>
          )}
        </section>
      )}
    </main>
  );
}
createRoot(document.getElementById('root')!).render(<App />);
