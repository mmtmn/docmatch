// /resultados?run=<id>: loads the run, follows its events until it finishes, then shows the
// findings. Decisions go to the feedback endpoint; XLSX/PDF come from the export endpoint.
import { useCallback, useEffect, useState } from 'react';
import { feedbackStatus, toResults } from '../../lib/adapt';
import { ApiError } from '../../lib/api';
import { t } from '../../lib/i18n';
import {
  exportUrl,
  getRun,
  listFindings,
  sendFeedback,
  watchRun,
  type RunView,
} from '../../lib/pipeline';
import { ResultsScreen } from './ResultsScreen';
import type { FindingStatus, RunResults } from './types';

type State =
  | { kind: 'missing' }
  | { kind: 'running'; message: string }
  | { kind: 'failed'; message: string }
  | { kind: 'done'; results: RunResults };

const POLL_MS = 3000;

function stageMessage(data: unknown): string | null {
  const m = (data as { message?: unknown } | undefined)?.message;
  return typeof m === 'string' && m ? m : null;
}

function runIdFromUrl(): string | null {
  return typeof window === 'undefined'
    ? null
    : new URLSearchParams(window.location.search).get('run');
}

// Rendered with client:only, so the URL is known on the first render.
export function ResultsLive() {
  const [runId] = useState(runIdFromUrl);
  const [state, setState] = useState<State>(() =>
    runId ? { kind: 'running', message: t('results.live_loading') } : { kind: 'missing' },
  );

  useEffect(() => {
    if (!runId) return undefined;
    const id = runId;
    let stopped = false;
    let close: () => void = () => {};
    let timer: ReturnType<typeof setTimeout> | undefined;

    const finish = async (run: RunView) => {
      if (stopped) return;
      if (run.status === 'completed') {
        close();
        const findings = await listFindings(run.id);
        if (!stopped) setState({ kind: 'done', results: toResults(run, findings) });
      } else if (run.status === 'failed' || run.status === 'cancelled') {
        close();
        setState({ kind: 'failed', message: run.error?.message ?? t('errors.unknown') });
      }
    };
    const refresh = async () => {
      if (timer) clearTimeout(timer);
      timer = undefined;
      try {
        const run = await getRun(id);
        await finish(run);
        if (
          !stopped &&
          (run.status === 'queued' || run.status === 'running' || run.status === 'needs_approval')
        ) {
          timer = setTimeout(() => void refresh(), POLL_MS);
        }
      } catch (err) {
        if (!stopped)
          setState({
            kind: 'failed',
            message: err instanceof ApiError ? err.message : t('errors.unknown'),
          });
      }
    };
    close = watchRun(id, (ev) => {
      if (ev.event === 'stage_started' || ev.event === 'progress') {
        const message = stageMessage(ev.data);
        if (message) setState((s) => (s.kind === 'running' ? { kind: 'running', message } : s));
      }
      if (ev.event === 'completed' || ev.event === 'failed') void refresh();
    });
    void refresh();
    return () => {
      stopped = true;
      close();
      if (timer) clearTimeout(timer);
    };
  }, [runId]);

  const onDecision = useCallback(
    (findingId: string, status: FindingStatus, note?: string) => {
      if (!runId) return;
      void sendFeedback(runId, findingId, feedbackStatus(status), note).catch(() => undefined);
    },
    [runId],
  );

  const onExport = useCallback(
    (format: 'xlsx' | 'pdf') => {
      const url = runId ? exportUrl(runId, format) : undefined;
      if (url) window.location.assign(url);
    },
    [runId],
  );

  switch (state.kind) {
    case 'missing':
      return <p>{t('results.live_missing')}</p>;
    case 'running':
      return <p role="status">{t('results.live_running', { message: state.message })}</p>;
    case 'failed':
      return <p role="alert">{t('results.live_failed', { message: state.message })}</p>;
    case 'done':
      return <ResultsScreen results={state.results} onDecision={onDecision} onExport={onExport} />;
  }
}
