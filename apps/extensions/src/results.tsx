import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { Organization } from '../../web/src/lib/accounts';
import type { Me } from '../../web/src/lib/session';
import type { ProjectDetail } from '../../web/src/lib/projects';
import { feedbackStatus, toResults } from '../../web/src/lib/adapt';
import {
  approveRun,
  cancelRun,
  downloadExport,
  exportFile,
  getRun,
  listFindings,
  sendFeedback,
  type QuoteView,
  type RunView,
} from '../../web/src/lib/pipeline';
import { ResultsScreen } from '../../web/src/components/results/ResultsScreen';
import type { FindingStatus, RunResults } from '../../web/src/components/results/types';
import { RunConfirmation } from '../../web/src/components/RunConfirmation';
import { ActionConfirmation } from '../../web/src/components/ActionConfirmation';
import { ProcessingScene } from '../../web/src/components/ProcessingScene';
import { t } from './i18n';
import type { boundRequest } from './client';
import { config } from './config';
import { browser } from './browser';

type State =
  | { kind: 'loading' }
  | { kind: 'running'; run: RunView }
  | { kind: 'done'; results: RunResults }
  | { kind: 'error'; message: string };
export function ProjectResults({
  runId,
  project,
  org,
  me,
  request,
  backHref,
}: {
  runId: string;
  project: ProjectDetail;
  org: Organization;
  me: Me;
  request: ReturnType<typeof boundRequest>;
  backHref: string;
}) {
  const [state, setState] = useState<State>({ kind: 'loading' });
  const [revision, setRevision] = useState(0);
  const [confirmation, setConfirmation] = useState<{ quote: QuoteView; key: string } | null>(null);
  const [cancel, setCancel] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [preparedExport, setPreparedExport] = useState<File | null>(null);
  const exportAnswer = useRef<{ resolve: () => void; reject: (error: unknown) => void } | null>(
    null,
  );
  const sharing = useRef(false);
  useEffect(
    () => () => {
      exportAnswer.current?.reject(new DOMException('Export closed', 'AbortError'));
      exportAnswer.current = null;
    },
    [],
  );
  const lock = useRef(false);
  const destination = useMemo(
    () => ({ orgId: org.id, projectId: project.id }),
    [org.id, project.id],
  );
  const context = {
    account: org.kind === 'personal' ? t('projects.personal_account') : org.name,
    project: project.name,
  };
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let fetching = false;
    const refresh = async () => {
      if (fetching || abort.signal.aborted) return;
      fetching = true;
      clearTimeout(timer);
      try {
        const run = await getRun(runId, abort.signal, destination, request);
        if (run.status === 'completed') {
          const findings = await listFindings(runId, abort.signal, destination, request);
          if (!abort.signal.aborted) setState({ kind: 'done', results: toResults(run, findings) });
        } else if (run.status === 'failed' || run.status === 'cancelled') {
          if (!abort.signal.aborted)
            setState({
              kind: 'error',
              message:
                run.status === 'cancelled'
                  ? t('projects.run_cancelled')
                  : (run.error?.message ?? t('errors.unknown')),
            });
        } else if (!abort.signal.aborted) {
          setState({ kind: 'running', run });
          timer = setTimeout(() => void refresh(), 3000);
        }
      } catch (cause) {
        if (!abort.signal.aborted)
          setState({
            kind: 'error',
            message: cause instanceof Error ? cause.message : t('errors.network'),
          });
      } finally {
        fetching = false;
      }
    };
    const foreground = () => {
      if (!document.hidden) void refresh();
    };
    window.addEventListener('focus', foreground);
    document.addEventListener('visibilitychange', foreground);
    void refresh();
    return () => {
      abort.abort();
      clearTimeout(timer);
      window.removeEventListener('focus', foreground);
      document.removeEventListener('visibilitychange', foreground);
    };
  }, [runId, destination, request, revision]);
  const decision = useCallback(
    async (id: string, status: FindingStatus, note?: string) => {
      await sendFeedback(runId, id, feedbackStatus(status), note, destination, request);
    },
    [runId, destination, request],
  );
  const exportResult = useCallback(
    async (format: 'csv' | 'xlsx' | 'pdf') => {
      const platform =
        config.browser === 'safari'
          ? ((await browser.runtime.sendNativeMessage?.('docmatch', { action: 'platform' })) as
              { os?: string } | undefined)
          : undefined;
      if (platform?.os === 'ios') {
        const file = await exportFile(runId, format, destination, request);
        if (!navigator.canShare?.({ files: [file] }))
          throw new Error(t('extensions.export_unavailable'));
        // Network work completes first. A fresh, explicit tap supplies the user
        // activation that Safari requires to open its Save to Files share sheet.
        await new Promise<void>((resolve, reject) => {
          exportAnswer.current = { resolve, reject };
          setPreparedExport(file);
        });
        return;
      }
      await downloadExport(runId, format, destination, request);
    },
    [runId, destination, request],
  );
  const action = async (approve: boolean) => {
    if (lock.current || !project.capabilities.reconcile) return;
    lock.current = true;
    setBusy(true);
    setError('');
    try {
      if (approve && confirmation)
        await approveRun(runId, confirmation.quote, confirmation.key, destination, request);
      else if (!approve) await cancelRun(runId, destination, request);
      setRevision((v) => v + 1);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('errors.unknown'));
    } finally {
      lock.current = false;
      setBusy(false);
      setConfirmation(null);
    }
  };
  return (
    <section className="extension-results">
      <p>
        <a href={backHref}>{t('projects.back_project')}</a> · {context.account} · {context.project}
      </p>
      {error && <p role="alert">{error}</p>}
      {preparedExport && (
        <ActionConfirmation
          message={t('extensions.export_ready')}
          description={t('extensions.export_save_hint')}
          confirmLabel={t('extensions.save_file')}
          onAnswer={(accepted) => {
            if (sharing.current) return;
            const answer = exportAnswer.current;
            if (!accepted) {
              exportAnswer.current = null;
              setPreparedExport(null);
              answer?.reject(new DOMException('Export cancelled', 'AbortError'));
              return;
            }
            sharing.current = true;
            void navigator
              .share({ files: [preparedExport] })
              .then(
                () => answer?.resolve(),
                (cause: unknown) => answer?.reject(cause),
              )
              .finally(() => {
                sharing.current = false;
                exportAnswer.current = null;
                setPreparedExport(null);
              });
          }}
        />
      )}
      {state.kind === 'loading' && <p role="status">{t('results.live_loading')}</p>}
      {state.kind === 'error' && (
        <div role="alert">
          <p>{state.message}</p>
          <button className="btn btn--ghost" onClick={() => setRevision((v) => v + 1)}>
            {t('projects.retry_load')}
          </button>
        </div>
      )}
      {state.kind === 'running' && (
        <>
          {state.run.status === 'needs_approval' ? (
            <section>
              <h1>{t('projects.run_approval')}</h1>
              <p>{t('projects.approval_needed')}</p>
              {project.capabilities.reconcile && state.run.pending_quote && (
                <button
                  className="btn btn--primary"
                  disabled={busy}
                  onClick={() => {
                    if (state.run.pending_quote)
                      setConfirmation({ quote: state.run.pending_quote, key: crypto.randomUUID() });
                  }}
                >
                  {t('projects.review_quote')}
                </button>
              )}
            </section>
          ) : (
            <div className="extension-processing">
              <ProcessingScene phase="reconcile" message={t('results.live_loading')} />
            </div>
          )}
          {project.capabilities.reconcile && (
            <button className="btn btn--ghost" disabled={busy} onClick={() => setCancel(true)}>
              {t('projects.cancel_run')}
            </button>
          )}
        </>
      )}
      {state.kind === 'done' && (
        <ResultsScreen
          results={state.results}
          onDecision={decision}
          onExport={exportResult}
          allowUndo={false}
          canDecide={project.capabilities.reconcile}
          backHref={backHref}
        />
      )}
      {cancel && (
        <ActionConfirmation
          message={t('projects.run_cancel_confirm')}
          onAnswer={(accepted) => {
            setCancel(false);
            if (accepted) void action(false);
          }}
        />
      )}
      {confirmation && (
        <RunConfirmation
          quote={confirmation.quote}
          exempt={me.credits?.token_exempt ?? false}
          context={context}
          onAnswer={(accepted) => {
            if (accepted) void action(true);
            else setConfirmation(null);
          }}
        />
      )}
    </section>
  );
}
