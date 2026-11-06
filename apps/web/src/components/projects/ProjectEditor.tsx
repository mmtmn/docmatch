import { useEffect, useRef, useState } from 'react';
import type { Organization } from '../../lib/accounts';
import { formatBytes } from '../../lib/format';
import { t, type MessageKey } from '../../lib/i18n';
import { textLimits } from '../../lib/limits';
import { uploadFile, type QuoteView, type RunView } from '../../lib/pipeline';
import { precheckFiles } from '../../lib/precheck';
import {
  type projectClient,
  projectResultUrl,
  type ProjectDetail,
  type ProjectDocument,
  type ProjectEvent,
} from '../../lib/projects';
import type { Me } from '../../lib/session';
import { RunConfirmation } from '../RunConfirmation';
import { ActionConfirmation } from '../ActionConfirmation';
import { ProjectRules } from './ProjectRules';
import { ProjectSharing } from './ProjectSharing';
import { useSavedProject } from './useSavedProject';
import { errorMessage, projectDate } from '../../lib/display';
import styles from './ProjectsApp.module.css';

const runLabels: Record<RunView['status'], MessageKey> = {
  queued: 'projects.run_queued',
  running: 'projects.run_running',
  needs_approval: 'projects.run_approval',
  completed: 'projects.run_completed',
  failed: 'projects.run_failed',
  cancelled: 'projects.run_cancelled',
};
const documentLabels: Record<ProjectDocument['status'], MessageKey> = {
  pending: 'projects.document_pending',
  scanning: 'projects.document_scanning',
  ready: 'projects.document_ready',
  rejected: 'projects.document_rejected',
  expired: 'projects.document_expired',
};
const eventLabels: Record<string, MessageKey> = {
  'project.created': 'projects.event_created',
  'project.updated': 'projects.event_updated',
  'project.archived': 'projects.event_archived',
  'project.restored': 'projects.event_restored',
  'project.sharing_changed': 'projects.event_shared',
  'document.added': 'projects.event_document',
  'document.removed': 'projects.event_removed',
  'document.replaced': 'projects.event_replaced',
  'run.created': 'projects.event_run',
};

export function ProjectEditor({
  initial,
  org,
  me,
  client,
}: {
  initial: ProjectDetail;
  org: Organization;
  me: Me;
  client: ReturnType<typeof projectClient>;
}) {
  const saved = useSavedProject(initial, client);
  const { project } = saved;
  const [name, setName] = useState(initial.name);
  const [settings, setSettings] = useState(
    initial.settings ?? { use_rules: true, web_search: false },
  );
  const [tab, setTab] = useState<'documents' | 'results' | 'activity'>('documents');
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState('');
  const [history, setHistory] = useState(false);
  const [confirm, setConfirm] = useState<{ message: string; action: () => Promise<void> } | null>(
    null,
  );
  const [quote, setQuote] = useState<{
    value: QuoteView;
    project: ProjectDetail;
    key: string;
  } | null>(null);
  const [expired, setExpired] = useState(false);
  const [reason, setReason] = useState('');
  const [runs, setRuns] = useState<RunView[] | null>(null);
  const [events, setEvents] = useState<ProjectEvent[] | null>(null);
  const picker = useRef<HTMLInputElement>(null);
  const replacementTarget = useRef<ProjectDocument | undefined>(undefined);
  const controller = useRef<AbortController | null>(null);
  const editable = project.capabilities.reconcile && !project.archived_at;
  const locked = busy || saved.status === 'conflict' || Boolean(quote);
  const accountName = org.kind === 'personal' ? t('projects.personal_account') : org.name;
  const activeDocuments = project.documents.filter(
    (doc) => !doc.removed_at && !doc.pending_replacement_id,
  );
  useEffect(() => () => controller.current?.abort(), []);
  useEffect(() => {
    if (!busy) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [busy]);

  useEffect(() => {
    if (tab === 'documents') return;
    const abort = new AbortController();
    const work =
      tab === 'results'
        ? client.runs(project.id, abort.signal).then((page) => setRuns(page.items))
        : client.activity(project.id, abort.signal).then((page) => setEvents(page.items));
    void work.catch((cause: unknown) => {
      if (!abort.signal.aborted) setError(errorMessage(cause));
    });
    return () => abort.abort();
  }, [client, project.id, project.version, tab]);

  const refresh = async () => {
    const next = await client.get(project.id);
    saved.replace(next);
    return next;
  };
  const perform = async (action: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    setNotice('');
    try {
      await action();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
      setProgress('');
    }
  };
  const addFiles = async (files: File[], previous?: ProjectDocument) => {
    if (!files.length || locked || !project.capabilities.upload || project.archived_at) return;
    await perform(async () => {
      if (previous && files.length !== 1) throw new Error(t('projects.replace_one'));
      await saved.save();
      const checked = await precheckFiles(files);
      const problems = checked.rejected.map((file) => file.reason);
      controller.current = new AbortController();
      try {
        for (const [index, file] of checked.accepted.entries()) {
          setProgress(t('app.uploading', { done: index + 1, total: checked.accepted.length }));
          try {
            const uploaded = await uploadFile(
              file,
              (fileName) => setProgress(t('app.scanning', { name: fileName })),
              controller.current.signal,
              {
                orgId: org.id,
                projectId: project.id,
                ...(previous ? { replacesId: previous.id } : {}),
              },
            );
            if (previous) {
              const current = await refresh();
              const replacement = current.documents.find((doc) => doc.upload_id === uploaded.id);
              if (!replacement) throw new Error(t('projects.replace_failed'));
              saved.replace(await client.replaceDocument(current, previous, replacement));
            }
          } catch (cause) {
            problems.push(`${file.name}: ${errorMessage(cause)}`);
          }
        }
      } finally {
        controller.current = null;
        await refresh();
      }
      if (problems.length) setError(problems.join('\n'));
      else setNotice(t('projects.upload_saved'));
    });
  };
  const estimate = async () =>
    perform(async () => {
      const current = await saved.save();
      const analysis = await client.estimate(current, reason.trim());
      if (analysis.run) {
        window.location.assign(projectResultUrl(org.id, project.id, analysis.run.id));
      } else if (analysis.state === 'expired') {
        setExpired(true);
      } else if (analysis.quote) {
        setQuote({ value: analysis.quote, project: current, key: crypto.randomUUID() });
      }
    });
  const reconcile = async () => {
    if (!quote) return;
    const approved = quote;
    setQuote(null);
    await perform(async () => {
      const analysis = await client.reconcile(
        approved.project,
        approved.value,
        approved.key,
        reason.trim(),
      );
      if (analysis.run)
        window.location.assign(projectResultUrl(org.id, project.id, analysis.run.id));
      else if (analysis.state === 'expired') setExpired(true);
    });
  };
  const ask = (message: string, action: () => Promise<void>) => setConfirm({ message, action });

  return (
    <>
      <a className={styles.back} href="/app">
        {t('projects.back')}
      </a>
      <header className={styles.heading}>
        <div className={styles.titleBlock}>
          {editable && <h1 className="visually-hidden">{project.name}</h1>}
          <label className="visually-hidden" htmlFor="saved-project-name">
            {t('projects.name')}
          </label>
          {editable ? (
            <input
              id="saved-project-name"
              className={styles.titleInput}
              value={name}
              maxLength={textLimits.project_name}
              disabled={locked}
              onChange={(event) => {
                setName(event.target.value);
                saved.change({ name: event.target.value });
              }}
              onBlur={() => {
                void saved.save().catch(() => undefined);
              }}
            />
          ) : (
            <h1>{project.name}</h1>
          )}
          <p className={styles.muted}>
            {accountName} ·{' '}
            {project.visibility === 'private' ? t('projects.private') : t('projects.shared')}
            {project.archived_at ? ` · ${t('projects.archived')}` : ''}
          </p>
        </div>
        <div className={styles.saveState} role="status" aria-live="polite">
          {saved.status === 'saved'
            ? t('projects.saved')
            : saved.status === 'saving'
              ? t('projects.saving')
              : saved.status === 'conflict'
                ? t('projects.conflict')
                : t('projects.unsaved')}
        </div>
      </header>
      {saved.error && (
        <div className={styles.notice} role="alert">
          <p>{saved.status === 'conflict' ? t('projects.conflict_help') : saved.error.message}</p>
          <button
            type="button"
            className="btn btn--ghost"
            onClick={() => {
              if (saved.status === 'conflict') window.location.reload();
              else void saved.save().catch(() => undefined);
            }}
          >
            {saved.status === 'conflict' ? t('projects.reload') : t('projects.retry_save')}
          </button>
        </div>
      )}
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
      {(progress || notice) && (
        <p role="status" className={styles.notice}>
          {progress || notice}
        </p>
      )}
      {!project.capabilities.read_results && (
        <p className={styles.notice}>{t('projects.upload_permission')}</p>
      )}
      <div
        className={styles.tabs}
        role="tablist"
        tabIndex={-1}
        aria-label={t('projects.sections')}
        onKeyDown={(event) => {
          const buttons = Array.from(
            event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'),
          );
          const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
          const next =
            event.key === 'ArrowRight'
              ? (index + 1) % buttons.length
              : event.key === 'ArrowLeft'
                ? (index + buttons.length - 1) % buttons.length
                : event.key === 'Home'
                  ? 0
                  : event.key === 'End'
                    ? buttons.length - 1
                    : -1;
          if (next >= 0) {
            event.preventDefault();
            buttons[next]?.focus();
            buttons[next]?.click();
          }
        }}
      >
        {(
          [
            'documents',
            ...(project.capabilities.read_results ? ['results', 'activity'] : []),
          ] as const
        ).map((value) => (
          <button
            key={value}
            type="button"
            role="tab"
            tabIndex={tab === value ? 0 : -1}
            id={`tab-${value}`}
            aria-controls={`panel-${value}`}
            aria-selected={tab === value}
            className="btn btn--ghost"
            onClick={() => setTab(value as typeof tab)}
          >
            {value === 'documents'
              ? t('projects.documents')
              : value === 'results'
                ? t('projects.results')
                : t('projects.activity')}
          </button>
        ))}
      </div>
      <section
        role="tabpanel"
        id={`panel-${tab}`}
        aria-labelledby={`tab-${tab}`}
        className={styles.panel}
      >
        {tab === 'documents' && (
          <>
            {project.capabilities.upload && !project.archived_at && (
              <div
                className={styles.dropzone}
                onDragOver={(event) => event.preventDefault()}
                onDrop={(event) => {
                  event.preventDefault();
                  void addFiles(Array.from(event.dataTransfer.files));
                }}
              >
                <p>{t('projects.drop_here')}</p>
                <small>
                  {t('projects.upload_destination', {
                    account: accountName,
                    project: project.name,
                  })}
                </small>
                <input
                  ref={picker}
                  type="file"
                  multiple
                  className="visually-hidden"
                  tabIndex={-1}
                  aria-label={t('projects.add_documents')}
                  disabled={locked}
                  onChange={(event) => {
                    const files = Array.from(event.currentTarget.files ?? []);
                    event.currentTarget.value = '';
                    const previous = replacementTarget.current;
                    replacementTarget.current = undefined;
                    void addFiles(files, previous);
                  }}
                />
                <button
                  type="button"
                  className="btn btn--primary"
                  disabled={locked}
                  onClick={() => {
                    replacementTarget.current = undefined;
                    picker.current?.click();
                  }}
                >
                  {t('projects.add_documents')}
                </button>
              </div>
            )}
            {activeDocuments.length === 0 && (
              <p className={styles.empty}>{t('projects.no_documents')}</p>
            )}
            <ul className={styles.documents}>
              {project.documents
                .filter((doc) => history || !doc.removed_at)
                .map((doc) => (
                  <li key={doc.id} className={doc.removed_at ? styles.removed : undefined}>
                    <div>
                      <strong>{doc.file_name}</strong>
                      <small>
                        {formatBytes(doc.size_bytes)} ·{' '}
                        {doc.removed_at
                          ? t('projects.document_removed')
                          : t(documentLabels[doc.status])}
                        {doc.replaces_id ? ` · ${t('projects.new_version')}` : ''}
                        {doc.pending_replacement_id
                          ? ` · ${t('projects.replacement_pending')}`
                          : ''}
                      </small>
                      {doc.expires_at && (
                        <small>
                          {t('projects.expires', { date: projectDate(doc.expires_at) })}
                        </small>
                      )}
                    </div>
                    {editable && !doc.removed_at && (
                      <div className={styles.actions}>
                        <button
                          type="button"
                          className="btn btn--ghost"
                          disabled={locked}
                          onClick={() => {
                            if (doc.pending_replacement_id) {
                              const previous = project.documents.find(
                                (item) => item.id === doc.pending_replacement_id,
                              );
                              if (previous)
                                void perform(async () => {
                                  const current = await saved.save();
                                  saved.replace(
                                    await client.replaceDocument(current, previous, doc),
                                  );
                                });
                            } else {
                              replacementTarget.current = doc;
                              picker.current?.click();
                            }
                          }}
                        >
                          {t(
                            doc.pending_replacement_id
                              ? 'projects.replacement_apply'
                              : 'projects.replace_document',
                          )}
                        </button>
                        <button
                          type="button"
                          className="btn btn--ghost"
                          disabled={locked}
                          onClick={() =>
                            ask(
                              t('projects.remove_document_confirm', { name: doc.file_name }),
                              async () => {
                                const current = await saved.save();
                                await client.removeDocument(current, doc);
                                await refresh();
                              },
                            )
                          }
                        >
                          {t('projects.remove_document')}
                        </button>
                      </div>
                    )}
                  </li>
                ))}
            </ul>
            {project.documents.some((doc) => doc.removed_at) && (
              <label className={styles.filter}>
                <input
                  type="checkbox"
                  checked={history}
                  onChange={(event) => setHistory(event.target.checked)}
                />
                {t('projects.show_versions')}
              </label>
            )}
            <p className={styles.muted}>{t('projects.retention')}</p>
            {project.capabilities.reconcile && (
              <fieldset disabled={locked || !editable} className={styles.settings}>
                <legend>{t('projects.settings')}</legend>
                <label htmlFor="project-rules">{t('projects.rules')}</label>
                <textarea
                  id="project-rules"
                  rows={4}
                  value={settings.rules_text ?? ''}
                  maxLength={textLimits.rules_text}
                  placeholder={t('projects.rules_example')}
                  onChange={(event) => {
                    const next = { ...settings, rules_text: event.target.value };
                    setSettings(next);
                    saved.change({ settings: next });
                  }}
                />
                <label className={styles.filter}>
                  <input
                    type="checkbox"
                    checked={settings.use_rules}
                    onChange={(event) => {
                      const next = { ...settings, use_rules: event.target.checked };
                      setSettings(next);
                      saved.change({ settings: next });
                    }}
                  />
                  {t('projects.use_rules')}
                </label>
                <label className={styles.filter}>
                  <input
                    type="checkbox"
                    checked={settings.web_search}
                    onChange={(event) => {
                      const next = { ...settings, web_search: event.target.checked };
                      setSettings(next);
                      saved.change({ settings: next });
                    }}
                  />
                  {t('projects.web_search')}
                </label>
              </fieldset>
            )}
            {project.capabilities.read_results && (
              <ProjectRules
                projectId={project.id}
                client={client}
                selected={settings.rule_ids ?? []}
                disabled={locked || !editable}
                onChange={(ids) => {
                  const next = { ...settings, rule_ids: ids };
                  setSettings(next);
                  saved.change({ settings: next });
                }}
              />
            )}
            {expired && (
              <div className={styles.notice}>
                <p>{t('projects.expired_result')}</p>
                <label htmlFor="reprocess-reason">{t('projects.reprocess_reason')}</label>
                <textarea
                  id="reprocess-reason"
                  rows={2}
                  maxLength={textLimits.reprocess_reason}
                  value={reason}
                  onChange={(event) => setReason(event.target.value)}
                />
              </div>
            )}
            {editable && (
              <div className={styles.analysis}>
                <div>
                  <strong>{t('projects.ready_to_reconcile')}</strong>
                  <p>{t('projects.no_duplicate_cost')}</p>
                </div>
                <button
                  type="button"
                  className="btn btn--primary"
                  disabled={locked || activeDocuments.length === 0 || (expired && !reason.trim())}
                  onClick={() => {
                    void estimate();
                  }}
                >
                  {t('projects.reconcile')}
                </button>
              </div>
            )}
          </>
        )}
        {tab === 'results' &&
          (runs === null ? (
            <p role="status">{t('mobile.loading')}</p>
          ) : runs.length === 0 ? (
            <p>{t('projects.no_results')}</p>
          ) : (
            <ul className={styles.list}>
              {runs.map((run) => (
                <li key={run.id}>
                  <a className={styles.card} href={projectResultUrl(org.id, project.id, run.id)}>
                    <strong>{t(runLabels[run.status])}</strong>
                    <small>{run.id}</small>
                  </a>
                </li>
              ))}
            </ul>
          ))}
        {tab === 'activity' &&
          (events === null ? (
            <p role="status">{t('mobile.loading')}</p>
          ) : (
            <ol className={styles.events}>
              {events.map((event) => (
                <li key={event.id}>
                  <span>{t(eventLabels[event.action] ?? 'projects.event_updated')}</span>
                  <time dateTime={event.created_at}>{projectDate(event.created_at)}</time>
                </li>
              ))}
            </ol>
          ))}
      </section>
      {project.capabilities.reconcile && (
        <details className={styles.manage}>
          <summary>{t('projects.manage')}</summary>
          {org.kind === 'team' && project.capabilities.manage_account && (
            <ProjectSharing
              key={`${project.visibility}:${(project.shared_with ?? []).join(',')}`}
              orgId={org.id}
              project={project}
              disabled={locked}
              onShare={(visibility, users) =>
                ask(t('projects.sharing_confirm'), async () => {
                  const current = await saved.save();
                  saved.replace(await client.share(current, visibility, users));
                })
              }
            />
          )}
          <div className={styles.actions}>
            <button
              type="button"
              className="btn btn--ghost"
              disabled={locked}
              onClick={() =>
                ask(
                  project.archived_at
                    ? t('projects.restore_confirm')
                    : t('projects.archive_confirm'),
                  async () => {
                    const current = await saved.save();
                    saved.replace(await client.update(current, { archived: !project.archived_at }));
                  },
                )
              }
            >
              {project.archived_at ? t('projects.restore') : t('projects.archive')}
            </button>
            <button
              type="button"
              className="btn btn--ghost"
              disabled={locked}
              onClick={() =>
                ask(t('projects.delete_confirm', { name: project.name }), async () => {
                  const current = await saved.save();
                  await client.remove(current);
                  window.location.assign('/app');
                })
              }
            >
              {t('projects.delete')}
            </button>
          </div>
        </details>
      )}
      {quote && (
        <RunConfirmation
          quote={quote.value}
          exempt={me.credits?.token_exempt ?? false}
          context={{ account: accountName, project: quote.project.name }}
          onAnswer={(approved) => {
            if (approved) void reconcile();
            else setQuote(null);
          }}
        />
      )}
      {confirm && (
        <ActionConfirmation
          message={confirm.message}
          onAnswer={(approved) => {
            const action = confirm.action;
            setConfirm(null);
            if (approved) void perform(action);
          }}
        />
      )}
    </>
  );
}
