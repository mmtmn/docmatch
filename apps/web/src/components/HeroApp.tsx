// Wires the HeroForm to the API when the build knows its address (PUBLIC_DM_API_BASE_URL):
// sign-in when needed (silently, on a development API that offers the local sign-in), upload and
// scan each file, one confirmation with the estimate, then the run's results page. Without an
// API the demo providers (PUBLIC_DM_FEATURE_RESULTS) or the plain form are used.
import { useCallback, useRef, useState } from 'react';
import { apiConfigured } from '../lib/api';
import { featureResults } from '../lib/env';
import { t } from '../lib/i18n';
import { mockQuoteProvider, mockRuleOptions } from '../lib/mock/quote';
import { createRun, estimate, uploadFile, type RunOptionsInput } from '../lib/pipeline';
import { currentUser, signInLocally } from '../lib/session';
import { AuthDialog } from './AuthDialog';
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
  const [authOpen, setAuthOpen] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const pending = useRef<RunRequest | null>(null);

  const run = useCallback(async (request: RunRequest) => {
    const ids: string[] = [];
    const total = request.files.length;
    for (const [i, file] of request.files.entries()) {
      setStatus(t('app.uploading', { done: i + 1, total }));
      const view = await uploadFile(file, (name) => setStatus(t('app.scanning', { name })));
      ids.push(view.id);
    }
    setStatus(t('app.estimating'));
    const opts = optionsOf(request);
    const quote = await estimate(ids, opts);
    if (
      !window.confirm(
        t('app.confirm_run', { expected: quote.expected_credits, cap: quote.cap_credits }),
      )
    ) {
      setStatus(null);
      throw new Error(t('app.cancelled'));
    }
    setStatus(t('app.starting'));
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
        await run(request);
      } catch (err) {
        setStatus(null);
        throw err;
      }
    },
    [run],
  );

  const resume = useCallback(() => {
    setAuthOpen(false);
    const request = pending.current;
    pending.current = null;
    if (!request) return;
    run(request).catch((err: unknown) => {
      setStatus(err instanceof Error ? err.message : t('errors.unknown'));
    });
  }, [run]);

  return (
    <>
      <HeroForm onSubmit={onSubmit} />
      {status ? (
        <p role="status" className={styles.status}>
          {status}
        </p>
      ) : null}
      <AuthDialog
        open={authOpen}
        onClose={() => {
          setAuthOpen(false);
          pending.current = null;
        }}
        onSignedIn={resume}
      />
    </>
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
