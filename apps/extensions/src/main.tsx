import { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { browser } from './browser';
import { config } from './config';
import type { APIRequest } from './policy';
import './style.css';
import { t } from './i18n';

interface Organization {
  id: string;
  name: string;
  kind: string;
  role: string;
}
interface Project {
  id: string;
  name: string;
  document_count: number;
}
async function message<T>(type: string, request?: APIRequest): Promise<T> {
  const response = (await browser.runtime.sendMessage({
    type,
    ...(request ? { request } : {}),
  })) as { ok: boolean; value?: T; error?: { code: string } };
  if (!response?.ok) throw new Error(response?.error?.code ?? 'connection_failed');
  return response.value as T;
}
function App() {
  const [signedIn, setSignedIn] = useState<boolean | null>(null);
  const [epoch, setEpoch] = useState('');
  const [accounts, setAccounts] = useState<Organization[]>([]);
  const [org, setOrg] = useState('');
  const [projects, setProjects] = useState<Project[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [revision, setRevision] = useState(0);
  const popup = window.location.pathname.endsWith('/popup.html');
  useEffect(() => {
    let active = true;
    void message<{ signedIn: boolean; epoch: string }>('status')
      .then((value) => {
        if (active) {
          setSignedIn(value.signedIn);
          setEpoch(value.epoch);
        }
      })
      .catch(() => {
        if (active) setError(t('extensions.open_failed'));
      });
    return () => {
      active = false;
    };
  }, [revision]);
  useEffect(() => {
    setAccounts([]);
    setProjects([]);
    setOrg('');
    if (!signedIn || !epoch) return;
    let active = true;
    void message<{ items: Organization[] }>('api', {
      path: '/v1/me/organizations',
      sessionEpoch: epoch,
    })
      .then((value) => {
        if (active) {
          setAccounts(value.items);
          setOrg(value.items[0]?.id ?? '');
        }
      })
      .catch(() => {
        if (active) {
          setSignedIn(false);
          setError(t('extensions.sign_in_again'));
        }
      });
    return () => {
      active = false;
    };
  }, [signedIn, epoch]);
  useEffect(() => {
    if (!org || !signedIn || !epoch) return;
    let active = true;
    setProjects([]);
    void message<{ items: Project[] }>('api', {
      path: '/v1/projects',
      orgId: org,
      sessionEpoch: epoch,
    })
      .then((value) => {
        if (active) setProjects(value.items);
      })
      .catch(() => {
        if (active) setError(t('extensions.projects_failed'));
      });
    return () => {
      active = false;
    };
  }, [org, epoch, signedIn]);
  const connect = async () => {
    setBusy(true);
    setError('');
    try {
      await message('signIn');
      setRevision((v) => v + 1);
    } catch {
      setError(
        config.browser === 'safari'
          ? t('extensions.safari_sign_in')
          : t('extensions.connection_failed'),
      );
    } finally {
      setBusy(false);
    }
  };
  const leave = async () => {
    setBusy(true);
    try {
      await message('signOut');
      setAccounts([]);
      setProjects([]);
      setOrg('');
      setSignedIn(false);
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className={popup ? 'popup' : 'workspace'}>
      <header>
        <strong>docmatch</strong>
        {config.local && <small>{t('extensions.local')}</small>}
      </header>
      <h1>{t('extensions.projects')}</h1>
      {error && <p role="alert">{error}</p>}
      {signedIn === null ? (
        <p role="status">{t('mobile.loading')}</p>
      ) : signedIn ? (
        <>
          <label>
            {t('extensions.account')}
            <select
              aria-label={t('extensions.account')}
              value={org}
              onChange={(e) => setOrg(e.target.value)}
            >
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </label>
          <ul>
            {projects.map((p) => (
              <li key={p.id}>
                <strong>{p.name}</strong>
                <span>{t('extensions.document_count', { count: p.document_count })}</span>
              </li>
            ))}
          </ul>
          {!projects.length && <p>{t('extensions.empty')}</p>}
          {popup && (
            <button
              onClick={() => {
                void browser.tabs.create({ url: browser.runtime.getURL('workspace.html') });
              }}
            >
              {t('extensions.open_projects')}
            </button>
          )}
          <button
            className="secondary"
            disabled={busy}
            onClick={() => {
              void leave();
            }}
          >
            {t('extensions.sign_out')}
          </button>
        </>
      ) : (
        <>
          <p>{t('extensions.connect_intro')}</p>
          <button
            disabled={busy}
            onClick={() => {
              void connect();
            }}
          >
            {busy ? t('extensions.connecting') : t('nav.login')}
          </button>
        </>
      )}
    </main>
  );
}
createRoot(document.getElementById('root')!).render(<App />);
