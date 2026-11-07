// Live results keep a fixed account and project for polling, approvals and exports.
import { useCallback, useEffect, useRef, useState } from 'react';
import { feedbackStatus, toResults } from '../../lib/adapt';
import { accounts } from '../../lib/accounts';
import { selectedAccount } from '../../lib/accountContext';
import { api, ApiError } from '../../lib/api';
import { t } from '../../lib/i18n';
import {
  approveRun,
  cancelRun,
  downloadExport,
  getRun,
  listFindings,
  sendFeedback,
  watchRun,
  type RunView,
  type RunDestination,
  type QuoteView,
} from '../../lib/pipeline';
import { projectClient } from '../../lib/projects';
import type { Me } from '../../lib/session';
import { ProcessingScene } from '../ProcessingScene';
import { RunConfirmation } from '../RunConfirmation';
import { ActionConfirmation } from '../ActionConfirmation';
import styles from './ResultsLive.module.css';
import { ResultsScreen } from './ResultsScreen';
import type { FindingStatus, RunResults } from './types';

type State =
  | { kind: 'missing' }
  | { kind: 'running'; message: string; run?: RunView }
  | { kind: 'failed'; message: string }
  | { kind: 'done'; results: RunResults };
const POLL_MS = 3000;

export function ResultsLive() {
  const [query] = useState(
    () => new URLSearchParams(typeof window === 'undefined' ? '' : window.location.search),
  );
  const runId = query.get('run');
  const [context, setContext] = useState<{
    destination: RunDestination;
    account: string;
    project: string;
    reconcile: boolean;
    exempt: boolean;
  } | null>(null);
  const [state, setState] = useState<State>(() =>
    runId ? { kind: 'running', message: t('results.live_loading') } : { kind: 'missing' },
  );
  const [confirmation, setConfirmation] = useState<{ quote: QuoteView; key: string } | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [cancelRequested, setCancelRequested] = useState(false);
  const [revision, setRevision] = useState(0);
  const actionPending = useRef(false);

  useEffect(() => {
    if (!runId) return;
    const controller = new AbortController();
    const { signal } = controller;
    void accounts(signal)
      .then(async (list) => {
        const requested = query.get('account') ?? selectedAccount()?.orgId;
        const org = list.items.find((item) => item.id === requested);
        if (!org || !org.capabilities.read_results)
          throw new ApiError(403, 'forbidden', t('projects.results_unavailable'));
        const projectId = query.get('project');
        const [me, project] = await Promise.all([
          api<Me>('/v1/me', { orgId: org.id, signal }),
          projectId ? projectClient(org.id).get(projectId, signal) : Promise.resolve(null),
        ]);
        if (signal.aborted) return;
        setContext({
          destination: { orgId: org.id, ...(projectId ? { projectId } : {}) },
          account: org.kind === 'personal' ? t('projects.personal_account') : org.name,
          project: project?.name ?? t('projects.legacy_run'),
          reconcile: project?.capabilities.reconcile ?? org.capabilities.reconcile,
          exempt: me.credits?.token_exempt ?? false,
        });
      })
      .catch((cause: unknown) => {
        if (!signal.aborted)
          setState({
            kind: 'failed',
            message: cause instanceof Error ? cause.message : t('errors.unknown'),
          });
      });
    return () => controller.abort();
  }, [query, runId]);

  useEffect(() => {
    if (!runId || !context) return;
    const controller = new AbortController();
    const { signal } = controller;
    const { destination } = context;
    let close = () => {};
    let timer: ReturnType<typeof setTimeout> | undefined;
    let refreshing = false;
    const refresh = async () => {
      if (refreshing || signal.aborted) return;
      refreshing = true;
      if (timer) clearTimeout(timer);
      try {
        const run = await getRun(runId, signal, destination);
        if (signal.aborted) return;
        if (run.status === 'completed') {
          close();
          const findings = await listFindings(run.id, signal, destination);
          if (!signal.aborted) setState({ kind: 'done', results: toResults(run, findings) });
        } else if (run.status === 'failed' || run.status === 'cancelled') {
          close();
          setState({
            kind: 'failed',
            message:
              run.status === 'cancelled'
                ? t('projects.run_cancelled')
                : (run.error?.message ?? t('errors.unknown')),
          });
        } else {
          setState((previous) => ({
            kind: 'running',
            run,
            message:
              run.status === 'needs_approval'
                ? t('projects.approval_needed')
                : previous.kind === 'running' && previous.run?.status === run.status
                  ? previous.message
                  : t('results.live_loading'),
          }));
          timer = setTimeout(() => {
            void refresh();
          }, POLL_MS);
        }
      } catch (cause) {
        if (!signal.aborted)
          setState({
            kind: 'failed',
            message: cause instanceof Error ? cause.message : t('errors.unknown'),
          });
      } finally {
        refreshing = false;
      }
    };
    close = watchRun(
      runId,
      (event) => {
        if (event.event === 'stage_started' || event.event === 'progress') {
          const message = (event.data as { message?: unknown } | undefined)?.message;
          if (typeof message === 'string' && message)
            setState((previous) =>
              previous.kind === 'running' ? { ...previous, message } : previous,
            );
        }
        if (
          event.event === 'completed' ||
          event.event === 'failed' ||
          event.event === 'needs_approval'
        )
          void refresh();
      },
      undefined,
      destination,
    );
    void refresh();
    return () => {
      controller.abort();
      close();
      if (timer) clearTimeout(timer);
    };
  }, [runId, context, revision]);

  const onDecision = useCallback(
    async (findingId: string, status: FindingStatus, note?: string) => {
      if (runId && context)
        await sendFeedback(runId, findingId, feedbackStatus(status), note, context.destination);
    },
    [runId, context],
  );
  const onExport = useCallback(
    async (format: 'csv' | 'xlsx' | 'pdf') => {
      if (runId && context) await downloadExport(runId, format, context.destination);
    },
    [runId, context],
  );
  const action = async (approve: boolean) => {
    if (!runId || !context || actionPending.current) return;
    actionPending.current = true;
    setBusy(true);
    setActionError(null);
    try {
      if (approve && confirmation)
        await approveRun(runId, confirmation.quote, confirmation.key, context.destination);
      else if (!approve) await cancelRun(runId, context.destination);
      setRevision((value) => value + 1);
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : t('errors.unknown'));
    } finally {
      setConfirmation(null);
      setBusy(false);
      actionPending.current = false;
    }
  };

  return (
    <>
      {state.kind !== 'done' &&
        (state.kind !== 'running' || state.run?.status !== 'needs_approval') && (
          <h1>{t('results.title')}</h1>
        )}
      {context && (
        <p>
          <a
            href={
              context.destination.projectId
                ? `/app?${new URLSearchParams({ account: context.destination.orgId, project: context.destination.projectId })}`
                : `/atividade?account=${encodeURIComponent(context.destination.orgId)}`
            }
          >
            {context.destination.projectId ? t('projects.back_project') : t('projects.activity')}
          </a>{' '}
          · {context.account} · {context.project}
        </p>
      )}
      {actionError && <p role="alert">{actionError}</p>}
      {state.kind === 'missing' && <p>{t('results.live_missing')}</p>}
      {state.kind === 'failed' && (
        <div role="alert">
          <p>{t('results.live_failed', { message: state.message })}</p>
          <button
            className="btn btn--ghost"
            type="button"
            onClick={() => {
              if (context) setRevision((value) => value + 1);
              else window.location.reload();
            }}
          >
            {t('projects.retry_load')}
          </button>
        </div>
      )}
      {state.kind === 'running' && (
        <>
          {state.run?.status === 'needs_approval' ? (
            <section>
              <h1>{t('projects.run_approval')}</h1>
              <p>{t('projects.approval_needed')}</p>
              {context?.reconcile && state.run.pending_quote && (
                <button
                  type="button"
                  className="btn btn--primary"
                  disabled={busy}
                  onClick={() => {
                    if (state.run?.pending_quote)
                      setConfirmation({ quote: state.run.pending_quote, key: crypto.randomUUID() });
                  }}
                >
                  {t('projects.review_quote')}
                </button>
              )}
              <p>{t('projects.approval_cancel')}</p>
            </section>
          ) : (
            <div className={styles.processing}>
              <ProcessingScene phase="reconcile" message={state.message} />
            </div>
          )}
          {context?.reconcile && state.run && (
            <button
              type="button"
              className="btn btn--ghost"
              disabled={busy}
              onClick={() => {
                setCancelRequested(true);
              }}
            >
              {t('projects.cancel_run')}
            </button>
          )}
        </>
      )}
      {state.kind === 'done' && (
        <ResultsScreen
          results={state.results}
          onDecision={onDecision}
          onExport={onExport}
          allowUndo={false}
        />
      )}
      {cancelRequested && (
        <ActionConfirmation
          message={t('projects.run_cancel_confirm')}
          onAnswer={(accepted) => {
            setCancelRequested(false);
            if (accepted) void action(false);
          }}
        />
      )}
      {confirmation && context && (
        <RunConfirmation
          quote={confirmation.quote}
          exempt={context.exempt}
          context={context}
          onAnswer={(confirmed) => {
            if (confirmed) void action(true);
            else setConfirmation(null);
          }}
        />
      )}
    </>
  );
}
