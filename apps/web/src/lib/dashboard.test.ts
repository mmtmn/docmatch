import { describe, it, expect } from 'vitest';
import {
  comparison,
  duration,
  matchRate,
  dashboardCsv,
  type DashboardMetrics,
  type DashboardData,
} from './dashboard';
const empty: DashboardMetrics = {
  runs: 0,
  completed: 0,
  failed: 0,
  cancelled: 0,
  queued: 0,
  running: 0,
  needs_approval: 0,
  credits_spent: 0,
  lines_total: 0,
  lines_matched: 0,
  findings: 0,
  high_findings: 0,
  median_seconds: null,
};
describe('dashboard numbers', () => {
  it('keeps missing measurements distinct from zero', () => {
    expect(matchRate(empty)).toBeNull();
    expect(matchRate({ ...empty, lines_total: 10 })).toBe(0);
    expect(matchRate({ ...empty, lines_total: 200, lines_matched: 150 })).toBe(75);
    expect(duration(null)).toBe('—');
    expect(duration(0)).toBe('0 s');
    expect(duration(90)).toBe('1,5 min');
    expect(duration(7200)).toBe('2 h');
  });
  it('does not imply an infinite change from an empty period', () => {
    expect(comparison(20, 0)).toBe('Sem base no período anterior');
    expect(comparison(10, 10)).toBe('Mesmo valor do período anterior');
    expect(comparison(20, 10)).toContain('+100%');
    expect(comparison(5, 10)).toContain('-50%');
  });
  it('exports the selected period and escapes spreadsheet formulas in account names', () => {
    const data = {
      current: { ...empty, completed: 12 },
      previous: { ...empty, completed: 8 },
      from: '2026-11-01T12:00:00Z',
      previous_from: '2026-10-25T12:00:00Z',
      generated_at: '2026-11-08T12:00:00Z',
    } as DashboardData;
    const csv = dashboardCsv(data, '=SUM(1;2)');
    expect(csv).toContain("'=SUM(1;2)");
    expect(csv).toContain('Conciliações concluídas;12;8');
    expect(csv).toContain(data.from);
  });
});
