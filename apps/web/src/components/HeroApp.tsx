import { WorkspaceShell } from './workspace/WorkspaceShell';
import { WorkspaceDialog } from './workspace/WorkspaceDialog';
import { useWorkspace } from '../lib/workspace';
// Wires the HeroForm to the API when the build knows its address (PUBLIC_DM_API_BASE_URL):
// sign-in when needed (silently, on a development API that offers the local sign-in), upload and
// scan each file, one confirmation with the estimate, then the run's results page. Without an
// API the demo providers (PUBLIC_DM_FEATURE_RESULTS) or the plain form are used.
import { useCallback, useEffect, useRef, useState } from 'react';
import { apiConfigured } from '../lib/api';
import { featureResults } from '../lib/env';
import { t } from '../lib/i18n';
import { mockQuoteProvider, mockRuleOptions } from '../lib/mock/quote';
import {
  createRun,
  estimate,
  uploadFile,
  type RunOptionsInput,
  type QuoteView,
} from '../lib/pipeline';
import { currentUser, signInLocally, type Me } from '../lib/session';
import { AuthDialog } from './AuthDialog';
import { RunConfirmation } from './RunConfirmation';
import type { ProcessingState } from './ProcessingScene';
import { HeroForm, type RunRequest } from './HeroForm';
import styles from './HeroApp.module.css';

const RESULTS_PATH = '/resultados';

function demoSubmit(): Promise<void> {
  window.location.assign(RESULTS_PATH);
  return Promise.resolve();
}

function optionsOf(request: RunRequest): RunOptionsInput {
  return {
    rulesText: request.rulesText,
    useRules: request.useRules,
    webSearch: request.webSearch,
    ruleIds: request.selectedRules,
  };
}

function LiveHero() {
  const workspace = useWorkspace();
  const [customizeOpen, setCustomizeOpen] = useState(false);
  const [authOpen, setAuthOpen] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [processing, setProcessing] = useState<ProcessingState | null>(null);
  const pending = useRef<RunRequest | null>(null);
  const [confirmation, setConfirmation] = useState<{ quote: QuoteView; exempt: boolean } | null>(
    null,
  );
  const answer = useRef<((confirmed: boolean) => void) | null>(null);
  useEffect(
    () => () => {
      answer.current?.(false);
      answer.current = null;
    },
    [],
  );
  const resolveConfirmation = useCallback((confirmed: boolean) => {
    const resolve = answer.current;
    answer.current = null;
    setConfirmation(null);
    resolve?.(confirmed);
  }, []);

  const run = useCallback(async (request: RunRequest, me: Me) => {
    setStatus(null);
    const ids: string[] = [];
    const total = request.files.length;
    for (const [i, file] of request.files.entries()) {
      setProcessing({ phase: 'upload', message: t('app.uploading', { done: i + 1, total }) });
      const view = await uploadFile(file, (name) =>
        setProcessing({ phase: 'scan', message: t('app.scanning', { name }) }),
      );
      ids.push(view.id);
    }
    setProcessing({ phase: 'estimate', message: t('app.estimating') });
    const opts = optionsOf(request);
    const quote = await estimate(ids, opts);
    const confirmed = await new Promise<boolean>((resolve) => {
      answer.current = resolve;
      setConfirmation({ quote, exempt: me.credits?.token_exempt ?? false });
    });
    if (!confirmed) {
      setStatus(null);
      throw new Error(t('app.cancelled'));
    }
    setProcessing({ phase: 'start', message: t('app.starting') });
    const created = await createRun(quote, ids, opts);
    window.location.assign(`${RESULTS_PATH}?run=${encodeURIComponent(created.id)}`);
  }, []);

  const onSubmit = useCallback(
    async (request: RunRequest) => {
      try {
        const me = (await currentUser()) ?? (await signInLocally());
        if (!me) {
          pending.current = request;
          setAuthOpen(true);
          return;
        }
        await run(request, me);
      } catch (err) {
        setProcessing(null);
        setStatus(null);
        throw err;
      }
    },
    [run],
  );

  const resume = useCallback(
    (me: Me) => {
      setAuthOpen(false);
      const request = pending.current;
      pending.current = null;
      if (!request) return;
      run(request, me).catch((err: unknown) => {
        setProcessing(null);
        setStatus(err instanceof Error ? err.message : t('errors.unknown'));
      });
    },
    [run],
  );

  return (
    <WorkspaceShell>
      <HeroForm
        onSubmit={onSubmit}
        processing={processing}
        secondaryAction={
          workspace && (
            <button type="button" className="btn btn--ghost" onClick={() => setCustomizeOpen(true)}>
              {t('workspace.open')}
            </button>
          )
        }
      />
      {customizeOpen && <WorkspaceDialog onClose={() => setCustomizeOpen(false)} />}
      {status ? (
        <p role="status" className={styles.status}>
          {status}
        </p>
      ) : null}
      {confirmation && (
        <RunConfirmation
          quote={confirmation.quote}
          exempt={confirmation.exempt}
          onAnswer={resolveConfirmation}
        />
      )}
      <AuthDialog
        open={authOpen}
        onClose={() => {
          setAuthOpen(false);
          pending.current = null;
        }}
        onSignedIn={resume}
      />
    </WorkspaceShell>
  );
}

export function HeroApp() {
  if (apiConfigured()) return <LiveHero />;
  if (!featureResults) return <HeroForm />;
  return (
    <HeroForm
      ruleOptions={mockRuleOptions}
      quoteProvider={mockQuoteProvider}
      onSubmit={demoSubmit}
    />
  );
}
