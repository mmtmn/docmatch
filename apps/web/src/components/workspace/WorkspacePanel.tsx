import { useEffect, useRef, useState } from 'react';
import { api } from '../../lib/api';
import { SESSION_CHANGED_EVENT } from '../../lib/session';
import { t } from '../../lib/i18n';
import { useWorkspace, workspaceChanged, type WorkspaceManifest } from '../../lib/workspace';
import styles from './WorkspacePanel.module.css';

type Proposal = {
  id: string;
  status: string;
  manifest?: WorkspaceManifest;
  reason?: string;
  credits: number;
};
const exampleKeys = [
  'workspace.example_dark',
  'workspace.example_group',
  'workspace.example_label',
  'workspace.example_sort',
] as const;
const escape = (s: string) =>
  s.replace(
    /[&<>"']/g,
    (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] ?? c,
  );
export function previewDocument(m: WorkspaceManifest): string {
  const origin = typeof window === 'undefined' ? '' : window.location.origin;
  return `<!doctype html><html lang="pt-BR" data-mode="${m.mode}" data-density="${m.density}"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${escape(origin)}; base-uri 'none'; form-action 'none'"><link rel="stylesheet" href="${escape(origin)}/workspace-preview.css"><title>${escape(t('workspace.preview'))}</title><h2>${escape(m.findings_label)}</h2><small>${escape(t('workspace.sample'))}</small><p>${escape(t(`workspace.group_${m.group_by}`))} · ${escape(t(`workspace.sort_${m.sort_by}`))}</p><article><strong>${escape(t('workspace.sample_title'))}</strong><p>${escape(t('workspace.sample_description'))}</p></article><p>${escape(t('workspace.format'))}: ${m.export_format.toUpperCase()}</p></html>`;
}
function displayValue(key: string, value: unknown): string {
  if (key === 'findings_label') return String(value);
  if (key === 'export_format') return String(value).toUpperCase();
  const prefix = key === 'group_by' ? 'group' : key === 'sort_by' ? 'sort' : key;
  return t(('workspace.' + prefix + '_' + String(value)) as Parameters<typeof t>[0]);
}
export function WorkspacePanel({ expanded = false }: { expanded?: boolean }) {
  const workspace = useWorkspace();
  const [request, setRequest] = useState('');
  const [proposal, setProposal] = useState<Proposal | null>(null);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const requestKey = useRef<{ text: string; key: string } | null>(null);
  const [rollback, setRollback] = useState<number | null>(null);
  useEffect(() => {
    const reset = () => {
      setRequest('');
      setProposal(null);
      setNotice('');
      setRollback(null);
      requestKey.current = null;
    };
    window.addEventListener(SESSION_CHANGED_EVENT, reset);
    return () => window.removeEventListener(SESSION_CHANGED_EVENT, reset);
  }, []);
  if (!workspace) return null;
  async function act(fn: () => Promise<void>) {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    setNotice('');
    try {
      await fn();
      workspaceChanged();
    } catch (e) {
      setNotice(e instanceof Error ? e.message : t('errors.unknown'));
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }
  return (
    <details className={styles.panel} open={expanded || undefined}>
      <summary>{t('workspace.title')}</summary>
      <div className={styles.body}>
        <p>{t('workspace.description')}</p>
        {!workspace.access.eligible ? (
          <p>
            <a href="/conta">{t('workspace.paid')}</a>
          </p>
        ) : (
          <>
            <p className="small muted">
              {t(workspace.access.limit < 0 ? 'workspace.unlimited' : 'workspace.quota', {
                used: workspace.access.used,
                limit: workspace.access.limit,
              })}
            </p>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                void act(async () => {
                  setProposal(null);
                  if (requestKey.current?.text !== request) {
                    requestKey.current = { text: request, key: crypto.randomUUID() };
                  }
                  const next = await api<Proposal>('/v1/customizations', {
                    body: { request },
                    idempotencyKey: requestKey.current.key,
                  });
                  if (next.status !== 'pending') requestKey.current = null;
                  setProposal(next);
                  if (next.status === 'pending') setNotice(t('workspace.preparing'));
                });
              }}
            >
              <label htmlFor="workspace-request">{t('workspace.prompt')}</label>
              <textarea
                id="workspace-request"
                value={request}
                onChange={(e) => setRequest(e.target.value)}
                maxLength={2000}
                minLength={3}
                required
                rows={3}
                disabled={busy}
              />
              <div className={styles.examples}>
                {exampleKeys.map((key) => (
                  <button
                    className="btn btn--ghost"
                    key={key}
                    type="button"
                    disabled={busy}
                    onClick={() => {
                      setRequest(t(key));
                      setProposal(null);
                    }}
                  >
                    {t(key)}
                  </button>
                ))}
              </div>
              <button
                type="submit"
                className="btn btn--primary"
                disabled={busy || !workspace.access.can_request || request.trim().length < 3}
              >
                {t(busy ? 'workspace.preparing' : 'workspace.prepare')}
              </button>
            </form>
            {proposal?.reason && <p role="status">{proposal.reason}</p>}
            {proposal?.status === 'planned' && proposal.manifest && (
              <section className={styles.preview} aria-label={t('workspace.preview')}>
                <h3>{t('workspace.preview')}</h3>
                {/* Preserve the origin for the local stylesheet's CORP policy. Scripts,
                    forms, navigation and popups remain disabled; labels are escaped. */}
                <iframe
                  title={t('workspace.preview')}
                  sandbox="allow-same-origin"
                  srcDoc={previewDocument(proposal.manifest)}
                />
                <dl>
                  {Object.entries(proposal.manifest)
                    .filter(
                      ([key, value]) =>
                        key !== 'version' &&
                        value !== workspace.manifest[key as keyof WorkspaceManifest],
                    )
                    .map(([key, value]) => (
                      <div key={key}>
                        <dt>{t(('workspace.field_' + key) as Parameters<typeof t>[0])}</dt>
                        <dd>
                          {displayValue(key, workspace.manifest[key as keyof WorkspaceManifest])} →{' '}
                          {displayValue(key, value)}
                        </dd>
                      </div>
                    ))}
                </dl>
                <p>{t('workspace.included')}</p>
                <button
                  type="button"
                  className="btn btn--primary"
                  disabled={busy}
                  onClick={() => {
                    void act(async () => {
                      await api(`/v1/customizations/${encodeURIComponent(proposal.id)}/approve`, {
                        body: {},
                        idempotent: true,
                      });
                      setProposal(null);
                      setNotice(t('workspace.applied'));
                    });
                  }}
                >
                  {t('workspace.approve')}
                </button>
              </section>
            )}
            {workspace.history.length > 1 && (
              <div className={styles.history}>
                <label htmlFor="workspace-history">{t('workspace.history')}</label>
                <select
                  id="workspace-history"
                  value={rollback ?? ''}
                  onChange={(e) => setRollback(Number(e.target.value) || null)}
                >
                  <option value="">{t('workspace.choose_version')}</option>
                  {workspace.history
                    .filter((v) => v.version !== workspace.version)
                    .map((v) => (
                      <option key={v.version} value={v.version}>
                        {t('workspace.version', { version: v.version })}
                      </option>
                    ))}
                </select>
                <button
                  className="btn btn--ghost"
                  type="button"
                  disabled={busy || rollback === null}
                  onClick={() => {
                    void act(async () => {
                      await api('/v1/workspace/rollback', {
                        body: { version: rollback },
                        idempotent: true,
                      });
                      setRollback(null);
                      setProposal(null);
                      setNotice(t('workspace.restored'));
                    });
                  }}
                >
                  {t('workspace.restore')}
                </button>
              </div>
            )}
          </>
        )}
        <p role="status" aria-live="polite">
          {notice}
        </p>
      </div>
    </details>
  );
}
