import { t, type MessageKey } from './i18n';
import { toCsv } from './csv';
import type { RunView } from './pipeline';

export type DashboardPeriod = 7 | 30 | 90;
export interface DashboardMetrics {
  runs: number;
  completed: number;
  failed: number;
  cancelled: number;
  queued: number;
  running: number;
  needs_approval: number;
  credits_spent: number;
  lines_total: number;
  lines_matched: number;
  findings: number;
  high_findings: number;
  median_seconds: number | null;
}
export interface DashboardDay {
  date: string;
  runs: number;
  completed: number;
  failed: number;
  credits_spent: number;
}
export interface DashboardRun {
  id: string;
  project_id: string;
  project_name: string;
  status: RunView['status'];
  created_at: string;
  expired: boolean;
  findings: number;
}
export interface DashboardProject {
  id: string;
  name: string;
  documents: number;
  updated_at: string;
  last_status: RunView['status'] | null;
  findings: number;
}
export interface DashboardData {
  generated_at: string;
  from: string;
  previous_from: string;
  days: DashboardPeriod;
  time_zone: string;
  current: DashboardMetrics;
  previous: DashboardMetrics;
  portfolio: { active: number; archived: number; documents: number; unavailable_documents: number };
  series: DashboardDay[];
  recent: DashboardRun[];
  attention: DashboardRun[];
  projects: DashboardProject[];
}
export const runLabels: Record<RunView['status'], MessageKey> = {
  queued: 'projects.run_queued',
  running: 'projects.run_running',
  needs_approval: 'projects.run_approval',
  completed: 'projects.run_completed',
  failed: 'projects.run_failed',
  cancelled: 'projects.run_cancelled',
};
export const dashboardNumber = (value: number) =>
  new Intl.NumberFormat('pt-BR', { maximumFractionDigits: 1 }).format(value);
export function matchRate(metrics: DashboardMetrics): number | null {
  return metrics.lines_total > 0 ? (100 * metrics.lines_matched) / metrics.lines_total : null;
}
export function comparison(current: number, previous: number): string {
  if (previous === 0) return t('dashboard.no_previous');
  if (current === previous) return t('dashboard.same');
  const value = ((current - previous) / previous) * 100;
  return t('dashboard.previous', { value: `${value > 0 ? '+' : ''}${dashboardNumber(value)}%` });
}
export function duration(seconds: number | null): string {
  if (seconds === null) return '—';
  if (seconds < 60) return t('dashboard.seconds', { value: dashboardNumber(seconds) });
  if (seconds < 3600) return t('dashboard.minutes', { value: dashboardNumber(seconds / 60) });
  return t('dashboard.hours', { value: dashboardNumber(seconds / 3600) });
}
export function dashboardCsv(data: DashboardData, account: string): string {
  const fields: [MessageKey, keyof DashboardMetrics][] = [
    ['dashboard.runs', 'runs'],
    ['dashboard.completed', 'completed'],
    ['dashboard.failed', 'failed'],
    ['dashboard.approval', 'needs_approval'],
    ['dashboard.tokens', 'credits_spent'],
    ['dashboard.findings', 'findings'],
    ['dashboard.matched', 'lines_matched'],
  ];
  return toCsv(
    [t('dashboard.export_metric'), t('dashboard.export_value'), t('dashboard.export_previous')],
    [
      [t('dashboard.export_scope'), account, ''],
      [t('dashboard.export_from'), data.from, data.previous_from],
      [t('dashboard.export_to'), data.generated_at, data.from],
      ...fields.map(([label, key]) => [
        t(label),
        data.current[key] ?? '',
        data.previous[key] ?? '',
      ]),
    ],
  );
}
