import { WorkspaceShell } from './workspace/WorkspaceShell';
import { WorkspaceDialog } from './workspace/WorkspaceDialog';
import { useWorkspace } from '../lib/workspace';
// Wires the HeroForm to the API when the build knows its address (PUBLIC_DM_API_BASE_URL):
// sign-in when needed (silently, on a development API that offers the local sign-in), upload and
// scan each file, one confirmation with the estimate, then the run's results page. Without an
// API the demo providers (PUBLIC_DM_FEATURE_RESULTS) or the plain form are used.
import { useCallback, useEffect, useRef, useState } from 'react';
import { api, apiConfigured } from '../lib/api';
import { accounts } from '../lib/accounts';
import { selectedAccount } from '../lib/accountContext';
import { projectClient, projectResultUrl } from '../lib/projects';
import { textLimits } from '../lib/limits';
import { featureResults } from '../lib/env';
import { t } from '../lib/i18n';
import { mockQuoteProvider, mockRuleOptions } from '../lib/mock/quote';
import { uploadFile, type QuoteView } from '../lib/pipeline';
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

function LiveHero() {
  const workspace = useWorkspace();
  const [customizeOpen, setCustomizeOpen] = useState(false);
  const [authOpen, setAuthOpen] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [processing, setProcessing] = useState<ProcessingState | null>(null);
  const pending = useRef<RunRequest | null>(null);
  const [confirmation, setConfirmation] = useState<{
    quote: QuoteView;
    exempt: boolean;
    context: { account: string; project: string };
  } | null>(null);
  const [savedProjectUrl, setSavedProjectUrl] = useState<string | null>(null);
  const work = useRef<{
    orgId: string;
    userId: string;
    projectId: string;
    files: string;
    uploaded: Set<string>;
  } | null>(null);
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

  const run = useCallback(async (request: RunRequest, _me: Me) => {
    setStatus(null);
    const list = await accounts();
    const org = list.items.find((item) => item.id === selectedAccount()?.orgId);
    if (!org) throw new Error(t('projects.no_account'));
    const me = await api<Me>('/v1/me', { orgId: org.id });
    const client = projectClient(org.id);
    const files = request.files
      .map((file) => file.id)
      .sort()
      .join(':');
    if (
      !work.current ||
      work.current.orgId !== org.id ||
      work.current.userId !== list.user_id ||
      work.current.files !== files
    ) {
      const suggested = Array.from(
        request.files[0]?.name.replace(/\.[^.]+$/, '') ?? t('projects.quick_name'),
      )
        .slice(0, textLimits.project_name)
        .join('');
      const created = await client.create(suggested);
      work.current = {
        orgId: org.id,
        userId: list.user_id,
        projectId: created.id,
        files,
        uploaded: new Set(),
      };
    }
    const current = work.current;
    setSavedProjectUrl(
      `/app?${new URLSearchParams({ account: org.id, project: current.projectId })}`,
    );
    const total = request.files.length;
    for (const [i, file] of request.files.entries()) {
      if (current.uploaded.has(file.id)) continue;
      setProcessing({ phase: 'upload', message: t('app.uploading', { done: i + 1, total }) });
      await uploadFile(
        file,
        (name) => setProcessing({ phase: 'scan', message: t('app.scanning', { name }) }),
        undefined,
        { orgId: org.id, projectId: current.projectId },
      );
      current.uploaded.add(file.id);
    }
    setProcessing({ phase: 'estimate', message: t('app.estimating') });
    let project = await client.get(current.projectId);
    const settings = {
      rules_text: request.rulesText.trim(),
      use_rules: request.useRules,
      web_search: request.webSearch,
      rule_ids: request.selectedRules,
    };
    project = await client.update(project, { settings });
    const analysis = await client.estimate(project);
    if (analysis.run) {
      window.location.assign(projectResultUrl(org.id, project.id, analysis.run.id));
      return;
    }
    if (!analysis.quote) {
      window.location.assign(
        `/app?${new URLSearchParams({ account: org.id, project: project.id })}`,
      );
      return;
    }
    const quote = analysis.quote;
    const confirmed = await new Promise<boolean>((resolve) => {
      answer.current = resolve;
      setConfirmation({
        quote,
        exempt: me.credits?.token_exempt ?? false,
        context: {
          account: org.kind === 'personal' ? t('projects.personal_account') : org.name,
          project: project.name,
        },
      });
    });
    if (!confirmed) {
      setStatus(null);
      throw new Error(t('app.cancelled'));
    }
    setProcessing({ phase: 'start', message: t('app.starting') });
    const created = await client.reconcile(project, quote, crypto.randomUUID());
    if (created.run) window.location.assign(projectResultUrl(org.id, project.id, created.run.id));
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
        persistDraft={false}
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
      {savedProjectUrl && (
        <p className={styles.status}>
          <a href={savedProjectUrl}>{t('projects.open_saved')}</a>
        </p>
      )}
      {confirmation && (
        <RunConfirmation
          quote={confirmation.quote}
          exempt={confirmation.exempt}
          context={confirmation.context}
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
