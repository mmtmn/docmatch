import { describe, expect, it } from 'vitest';
import { decisionStatus, feedbackStatus, toResults } from './adapt';
import type { FindingView, RunView } from './pipeline';

const run: RunView = {
  id: 'run_1',
  status: 'completed',
  sources: [
    { id: 'upl_bank', file_name: 'extrato.csv', status: 'ready' },
    { id: 'upl_erp', file_name: 'titulos.csv', status: 'ready' },
  ],
};

const finding: FindingView = {
  id: 'fnd_1',
  code: 'recon.partial_payment',
  kind: 'partial',
  severity: 'high',
  amount_at_risk: 4_900_000,
  currency: 'BRL',
  title: 'Pagamento parcial de exatamente 50%',
  explanation: 'Metade recebida.',
  confidence: 0.97,
  confidence_label: 'Confirmado',
  status: 'open',
  evidence: [
    {
      source_id: 'upl_bank',
      locator: { type: 'row', row: 7 },
      label: 'Linha 7 do extrato',
      values: { date: '2026-07-16', description: 'LIQUIDACAO', amount: 4_900_000 },
    },
    {
      source_id: 'upl_erp',
      locator: { type: 'row', row: 7 },
      label: 'NF 1',
      values: { date: '2026-07-16', description: 'Cliente', amount: 9_800_000, document: 'NF 1' },
    },
  ],
};

describe('toResults', () => {
  it('puts each source file on its own side, in reais', () => {
    const r = toResults(run, [finding]);
    expect(r.fileCount).toBe(2);
    const f = r.findings[0];
    expect(f?.amountAtRisk).toBe(49_000);
    expect(f?.confidence).toBe('high');
    expect(f?.matchType).toBe('amount');
    expect(f?.left).toMatchObject({ kind: 'table', sourceName: 'extrato.csv' });
    expect(f?.right).toMatchObject({ kind: 'table', sourceName: 'titulos.csv' });
    if (f?.right.kind === 'table')
      expect(f.right.rows[0]).toMatchObject({ amount: 98_000, document: 'NF 1', cited: true });
  });

  it('reads the pt-BR confidence labels the API sends', () => {
    const levels = ['Confirmado', 'Provável', 'Precisa de você'].map(
      (label) => toResults(run, [{ ...finding, confidence_label: label }]).findings[0]?.confidence,
    );
    expect(levels).toEqual(['high', 'medium', 'low']);
  });

  it('maps duplicates, missing items and dates', () => {
    const kinds = [
      { ...finding, kind: 'duplicate' },
      { ...finding, kind: 'missing' },
      { ...finding, kind: 'note', code: 'recon.early_payment' },
    ];
    expect(toResults(run, kinds).findings.map((f) => f.matchType)).toEqual([
      'duplicate',
      'missing',
      'date',
    ]);
  });

  it('round-trips decision statuses', () => {
    for (const s of ['open', 'accepted', 'rejected', 'edited'] as const) {
      expect(decisionStatus(feedbackStatus(s))).toBe(s);
    }
  });
});
