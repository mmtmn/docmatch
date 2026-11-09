import { useEffect, useId, useState } from 'react';
import { accounts, type Organization } from '../../lib/accounts';
import { ACCOUNT_CHANGED_EVENT, selectedAccount } from '../../lib/accountContext';
import { api, ApiError } from '../../lib/api';
import { downloadText } from '../../lib/csv';
import {
  comparison,
  dashboardCsv,
  dashboardNumber as number,
  duration,
  matchRate,
  runLabels,
  type DashboardData,
  type DashboardPeriod,
  type DashboardRun,
} from '../../lib/dashboard';
import { projectDate } from '../../lib/display';
import { t } from '../../lib/i18n';
import { SESSION_CHANGED_EVENT } from '../../lib/session';
import styles from './DashboardApp.module.css';
import nav from '../projects/ProjectsApp.module.css';

export function DashboardApp() {
  const [days, setDays] = useState<DashboardPeriod>(30);
  const [refresh, setRefresh] = useState(0);
  const [state, setState] = useState<{
    org: Organization | null;
    data: DashboardData | null;
    mode: 'loading' | 'ready' | 'error' | 'signed_out' | 'restricted';
  }>({ org: null, data: null, mode: 'loading' });
  useEffect(() => {
    let controller: AbortController | undefined;
    const load = () => {
      controller?.abort();
      controller = new AbortController();
      const { signal } = controller;
      setState({ org: null, data: null, mode: 'loading' });
      void accounts(signal)
        .then(async (list) => {
          const org = list.items.find((item) => item.id === selectedAccount()?.orgId);
          if (!org) throw new Error('No account');
          if (signal.aborted) return;
          if (!org.capabilities.read_results) {
            setState({ org, data: null, mode: 'restricted' });
            return;
          }
          setState({ org, data: null, mode: 'loading' });
          const data = await api<DashboardData>(`/v1/projects/dashboard?days=${days}`, {
            orgId: org.id,
            signal,
          });
          if (!signal.aborted) setState({ org, data, mode: 'ready' });
        })
        .catch((error: unknown) => {
          if (signal.aborted) return;
          setState((previous) => ({
            org: previous.org,
            data: null,
            mode:
              error instanceof ApiError && error.status === 401
                ? 'signed_out'
                : error instanceof ApiError && error.status === 403
                  ? 'restricted'
                  : 'error',
          }));
        });
    };
    load();
    window.addEventListener(SESSION_CHANGED_EVENT, load);
    window.addEventListener(ACCOUNT_CHANGED_EVENT, load);
    return () => {
      controller?.abort();
      window.removeEventListener(SESSION_CHANGED_EVENT, load);
      window.removeEventListener(ACCOUNT_CHANGED_EVENT, load);
    };
  }, [days, refresh]);
  const { org, data, mode } = state;
  const account = org?.kind === 'personal' ? t('projects.personal_account') : org?.name;
  return (
    <section className={styles.page}>
      <nav className={nav.navigation} aria-label={t('projects.navigation')}>
        <a href="/painel" aria-current="page">
          {t('dashboard.nav')}
        </a>
        <a href="/app">{t('projects.title')}</a>
        {org?.capabilities.read_results && <a href="/atividade">{t('projects.activity')}</a>}
        <a href={org?.capabilities.manage_account ? '/equipe' : '/conta'}>
          {org?.capabilities.manage_account ? t('projects.team_plan') : t('projects.my_profile')}
        </a>
      </nav>
      <header className={styles.heading}>
        <div>
          <p className={styles.eyebrow}>{account ?? t('dashboard.nav')}</p>
          <h1>{t('dashboard.title')}</h1>
          <p className={styles.muted}>{t('dashboard.intro')}</p>
        </div>
        {org?.capabilities.read_results && (
          <div className={styles.controls}>
            <label>
              <span className="visually-hidden">{t('dashboard.period')}</span>
              <select
                value={days}
                onChange={(event) => setDays(Number(event.target.value) as DashboardPeriod)}
              >
                {([7, 30, 90] as const).map((value) => (
                  <option key={value} value={value}>
                    {t('dashboard.days', { days: value })}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              className="btn btn--ghost"
              disabled={mode === 'loading'}
              onClick={() => setRefresh((value) => value + 1)}
            >
              {t('dashboard.refresh')}
            </button>
          </div>
        )}
      </header>
      {mode === 'signed_out' ? (
        <div className={styles.empty}>
          <h2>{t('dashboard.nav')}</h2>
          <p>{t('dashboard.sign_in')}</p>
          <a className="btn btn--primary" href="/login?next=%2Fpainel">
            {t('nav.login')}
          </a>
        </div>
      ) : mode === 'restricted' ? (
        <div className={styles.empty}>
          <p>{t('dashboard.restricted')}</p>
          <a className="btn btn--primary" href="/app">
            {t('dashboard.projects_action')}
          </a>
        </div>
      ) : mode === 'error' ? (
        <div className={styles.empty}>
          <p role="alert">{t('dashboard.error')}</p>
          <button
            type="button"
            className="btn btn--primary"
            onClick={() => setRefresh((value) => value + 1)}
          >
            {t('dashboard.retry')}
          </button>
        </div>
      ) : !data ? (
        <div className={styles.loading} aria-busy="true">
          <p role="status">{t('dashboard.loading')}</p>
          <div className={styles.skeleton} aria-hidden="true" />
        </div>
      ) : (
        <>
          <div className={styles.context}>
            <p>{t('dashboard.scope')}</p>
            <p role="status">
              {t('dashboard.updated', {
                time: new Intl.DateTimeFormat('pt-BR', {
                  hour: '2-digit',
                  minute: '2-digit',
                  timeZone: data.time_zone,
                }).format(new Date(data.generated_at)),
              })}
            </p>
          </div>
          {data.current.runs === 0 && (
            <div className={styles.welcome}>
              <div>
                <h2>
                  {t(
                    data.portfolio.active === 0
                      ? 'dashboard.empty_title'
                      : 'dashboard.empty_period',
                  )}
                </h2>
                <p>
                  {t(
                    data.portfolio.active === 0
                      ? 'dashboard.empty_body'
                      : 'dashboard.empty_period_body',
                  )}
                </p>
              </div>
              <a className="btn btn--primary" href="/app">
                {t('dashboard.projects_action')}
              </a>
            </div>
          )}
          <Overview data={data} />
          <div className={styles.mainGrid}>
            <Trend data={data} />
            <Performance data={data} />
          </div>
          <Attention data={data} orgId={org?.id ?? ''} />
          <div className={styles.bottomGrid}>
            <section className={styles.panel} aria-labelledby="dashboard-projects">
              <div className={styles.panelHeading}>
                <div>
                  <h2 id="dashboard-projects">{t('dashboard.project_overview')}</h2>
                  <p>{t('dashboard.project_overview_intro')}</p>
                </div>
                <a href="/app">{t('dashboard.all_projects')}</a>
              </div>
              <div className={styles.portfolio}>
                <div>
                  <strong>{number(data.portfolio.active)}</strong>
                  <span>{t('dashboard.active')}</span>
                </div>
                <div>
                  <strong>{number(data.portfolio.documents)}</strong>
                  <span>{t('dashboard.documents')}</span>
                </div>
              </div>
              <ul className={styles.projectList}>
                {data.projects.map((project) => (
                  <li key={project.id}>
                    <a
                      href={`/app?${new URLSearchParams({ account: org?.id ?? '', project: project.id })}`}
                    >
                      <div>
                        <strong>{project.name}</strong>
                        <span>{t('dashboard.document_count', { count: project.documents })}</span>
                      </div>
                      <span className={styles.projectState}>
                        {project.last_status
                          ? t(runLabels[project.last_status])
                          : t('dashboard.not_analyzed')}
                        {project.last_status === 'completed' && (
                          <small>
                            {t('dashboard.project_findings', { count: project.findings })}
                          </small>
                        )}
                      </span>
                    </a>
                  </li>
                ))}
              </ul>
              {data.projects.length === 0 && (
                <p className={styles.muted}>{t('dashboard.empty_projects')}</p>
              )}
              {data.portfolio.archived > 0 && (
                <p className={styles.muted}>
                  {t('dashboard.archived', { count: data.portfolio.archived })}
                </p>
              )}
            </section>
            <section className={styles.panel} aria-labelledby="dashboard-recent">
              <div className={styles.panelHeading}>
                <div>
                  <h2 id="dashboard-recent">{t('dashboard.recent')}</h2>
                  <p>{t('dashboard.recent_intro')}</p>
                </div>
                <a href="/atividade">{t('dashboard.activity_action')}</a>
              </div>
              <ul className={styles.recentList}>
                {data.recent.map((run) => (
                  <li key={run.id}>
                    <RunLink run={run} orgId={org?.id ?? ''} />
                  </li>
                ))}
              </ul>
              {data.recent.length === 0 && (
                <p className={styles.muted}>{t('dashboard.empty_period')}</p>
              )}
            </section>
          </div>
          <footer className={styles.notes}>
            <details>
              <summary>{t('dashboard.definitions')}</summary>
              <p>{t('dashboard.definitions_body')}</p>
              <p>{t('dashboard.definition_time')}</p>
            </details>
            <button
              type="button"
              className="btn btn--ghost"
              onClick={() =>
                downloadText(
                  `${t('dashboard.export_name')}-${data.generated_at.slice(0, 10)}.csv`,
                  dashboardCsv(data, account ?? ''),
                )
              }
            >
              {t('dashboard.export')}
            </button>
          </footer>
        </>
      )}
    </section>
  );
}

function Overview({ data }: { data: DashboardData }) {
  const current = data.current;
  const rate = matchRate(current);
  const cards = [
    {
      label: t('dashboard.completed'),
      value: number(current.completed),
      detail: t('dashboard.completed_detail', { count: current.runs }),
      note: comparison(current.completed, data.previous.completed),
    },
    {
      label: t('dashboard.matched'),
      value: rate === null ? '—' : `${number(rate)}%`,
      detail:
        rate === null
          ? t('dashboard.no_records')
          : t('dashboard.matched_detail', {
              matched: current.lines_matched,
              total: current.lines_total,
            }),
      note: null,
    },
    {
      label: t('dashboard.findings'),
      value: number(current.findings),
      detail: t('dashboard.findings_detail', { count: current.high_findings }),
      note: null,
    },
    {
      label: t('dashboard.tokens'),
      value: number(current.credits_spent),
      detail: t('dashboard.tokens_detail'),
      note: comparison(current.credits_spent, data.previous.credits_spent),
    },
  ];
  return (
    <section className={styles.metrics} aria-label={t('dashboard.summary')}>
      {cards.map((card) => (
        <article className={styles.metric} key={card.label}>
          <h2>{card.label}</h2>
          <strong>{card.value}</strong>
          <p>{card.detail}</p>
          {card.note && <small>{card.note}</small>}
        </article>
      ))}
    </section>
  );
}

// The scrollable data table is a named region that needs focus for keyboard scrolling.
/* eslint-disable jsx-a11y-x/no-noninteractive-tabindex */
function Trend({ data }: { data: DashboardData }) {
  const [metric, setMetric] = useState<'runs' | 'credits_spent'>('runs');
  const id = useId();
  const values = data.series.map((day) => day[metric]);
  const maximum = Math.max(2, ...values);
  const points = values.map(
    (value, index) =>
      `${(index * 580) / Math.max(1, values.length - 1)},${140 - (value * 140) / maximum}`,
  );
  const label = t(metric === 'runs' ? 'dashboard.volume' : 'dashboard.tokens');
  const total = data.current[metric];
  const date = (value: string) =>
    new Intl.DateTimeFormat('pt-BR', { day: '2-digit', month: 'short', timeZone: 'UTC' }).format(
      new Date(`${value}T12:00:00Z`),
    );
  return (
    <section className={`${styles.panel} ${styles.trend}`} aria-labelledby={`${id}-heading`}>
      <div className={styles.panelHeading}>
        <div>
          <h2 id={`${id}-heading`}>{t('dashboard.activity')}</h2>
          <p>{t('dashboard.activity_intro')}</p>
        </div>
        <div className={styles.segment} aria-label={t('dashboard.activity')}>
          {(['runs', 'credits_spent'] as const).map((key) => (
            <button
              type="button"
              key={key}
              aria-pressed={key === metric}
              onClick={() => setMetric(key)}
            >
              {t(key === 'runs' ? 'dashboard.volume' : 'dashboard.tokens')}
            </button>
          ))}
        </div>
      </div>
      <div className={styles.chartTotal}>
        <strong>{number(total)}</strong>
        <span>{label.toLocaleLowerCase('pt-BR')}</span>
      </div>
      <div className={styles.plot}>
        <div className={styles.axis} aria-hidden="true">
          {[1, 0.5, 0].map((step) => (
            <span key={step}>
              {new Intl.NumberFormat('pt-BR', {
                notation: 'compact',
                maximumFractionDigits: 1,
              }).format(maximum * step)}
            </span>
          ))}
        </div>
        <svg
          className={styles.chart}
          viewBox="0 0 580 140"
          preserveAspectRatio="none"
          role="img"
          aria-labelledby={`${id}-title`}
        >
          <title id={`${id}-title`}>{t('dashboard.chart_label', { metric: label, total })}</title>
          {[0, 0.5, 1].map((step) => (
            <line
              key={step}
              x1="0"
              x2="580"
              y1={140 - step * 140}
              y2={140 - step * 140}
              stroke="var(--color-border)"
              strokeDasharray={step === 0 ? undefined : '3 5'}
              vectorEffect="non-scaling-stroke"
            />
          ))}
          <path d={`M0,140 L${points.join(' L')} L580,140 Z`} fill="var(--color-accent-soft)" />
          <polyline
            points={points.join(' ')}
            fill="none"
            stroke="var(--color-accent)"
            strokeWidth="2.5"
            strokeLinejoin="round"
            vectorEffect="non-scaling-stroke"
          />
        </svg>
      </div>
      <div className={styles.dates} aria-hidden="true">
        <span>{data.series[0] && date(data.series[0].date)}</span>
        <span>{data.series.at(-1) && date(data.series.at(-1)!.date)}</span>
      </div>
      <details className={styles.dataTable}>
        <summary>{t('dashboard.table')}</summary>
        <div
          className={styles.tableScroll}
          tabIndex={0}
          role="region"
          aria-label={t('dashboard.table')}
        >
          <table>
            <caption className="visually-hidden">{t('dashboard.activity')}</caption>
            <thead>
              <tr>
                {[
                  'dashboard.date',
                  'dashboard.runs',
                  'dashboard.completed',
                  'dashboard.failed',
                  'dashboard.tokens',
                ].map((key) => (
                  <th key={key} scope="col">
                    {t(key as 'dashboard.date')}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.series.map((day) => (
                <tr key={day.date}>
                  <th scope="row">{date(day.date)}</th>
                  <td>{number(day.runs)}</td>
                  <td>{number(day.completed)}</td>
                  <td>{number(day.failed)}</td>
                  <td>{number(day.credits_spent)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </section>
  );
}

/* eslint-enable jsx-a11y-x/no-noninteractive-tabindex */

function Performance({ data }: { data: DashboardData }) {
  const current = data.current;
  return (
    <section
      className={`${styles.panel} ${styles.performance}`}
      aria-labelledby="dashboard-performance"
    >
      <h2 id="dashboard-performance">{t('dashboard.processing')}</h2>
      <p className={styles.muted}>{t('dashboard.processing_intro')}</p>
      <div className={styles.duration}>
        <span>{t('dashboard.median')}</span>
        <strong>{duration(current.median_seconds)}</strong>
        <small>
          {t(current.median_seconds === null ? 'dashboard.no_duration' : 'dashboard.median_detail')}
        </small>
      </div>
      <dl>
        <div>
          <dt>{t('dashboard.in_progress')}</dt>
          <dd>{number(current.queued + current.running)}</dd>
        </div>
        <div>
          <dt>{t('dashboard.approval')}</dt>
          <dd>{number(current.needs_approval)}</dd>
        </div>
        <div>
          <dt>{t('dashboard.failed')}</dt>
          <dd>{number(current.failed)}</dd>
        </div>
      </dl>
      <p className={styles.muted}>
        {t('dashboard.in_progress_detail', { queued: current.queued, running: current.running })}
      </p>
    </section>
  );
}

function RunLink({ run, orgId }: { run: DashboardRun; orgId: string }) {
  const name = run.project_name || t('dashboard.quick_run');
  const content = (
    <>
      <div>
        <strong>{name}</strong>
        <span>{projectDate(run.created_at)}</span>
      </div>
      <span className={styles.badge} data-status={run.expired ? 'expired' : run.status}>
        {run.expired ? t('dashboard.expired') : t(runLabels[run.status])}
      </span>
    </>
  );
  if (run.expired) return <div className={styles.run}>{content}</div>;
  const query = new URLSearchParams({ account: orgId, run: run.id });
  if (run.project_id) query.set('project', run.project_id);
  return (
    <a className={styles.run} href={`/resultados?${query}`}>
      {content}
    </a>
  );
}

function Attention({ data, orgId }: { data: DashboardData; orgId: string }) {
  const attention = data.attention.length > 0 || data.portfolio.unavailable_documents > 0;
  return (
    <section
      className={`${styles.panel} ${styles.attention}`}
      data-attention={attention}
      aria-labelledby="dashboard-attention"
    >
      <div className={styles.panelHeading}>
        <div>
          <h2 id="dashboard-attention">{t('dashboard.attention')}</h2>
          <p>{t('dashboard.attention_intro')}</p>
        </div>
      </div>
      {!attention ? (
        <div className={styles.calm}>
          <span aria-hidden="true">✓</span>
          <div>
            <strong>{t('dashboard.clear')}</strong>
            <p>{t('dashboard.clear_detail')}</p>
          </div>
        </div>
      ) : (
        <ul className={styles.attentionList}>
          {data.attention.map((run) => (
            <li key={run.id}>
              <RunLink run={run} orgId={orgId} />
            </li>
          ))}
          {data.portfolio.unavailable_documents > 0 && (
            <li>
              <a className={styles.run} href="/app">
                <div>
                  <strong>
                    {t('dashboard.unavailable', { count: data.portfolio.unavailable_documents })}
                  </strong>
                  <span>{t('dashboard.unavailable_detail')}</span>
                </div>
                <span aria-hidden="true">→</span>
              </a>
            </li>
          )}
        </ul>
      )}
    </section>
  );
}
