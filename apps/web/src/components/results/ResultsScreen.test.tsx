import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { t } from '../../lib/i18n';
import { mockResults } from '../../lib/mock/results';
import type { ReconciliationDocument } from '../../lib/pipeline';
import { MediaPlayer } from './EvidencePanel';
import { DocumentRowEffect } from './ReconciledDocument';
import { findingsCsv, ResultsScreen, sortByRisk } from './ResultsScreen';
import type { RunResults } from './types';

const paid = {
  id: 'T1',
  reference: 'NF 1',
  customer: 'Cliente exemplo',
  source_id: 'source_1',
  source_name: 'carteira.csv',
  source_row: 2,
  due_date: '2026-07-01',
  paid_on: '2026-07-02',
  original_amount: 10000,
  received_amount: 10000,
  applied_principal: 10000,
  open_amount: 0,
  proposed_amount: 0,
  status: 'paid',
  duplicate_of: '',
};
const proposed = {
  ...paid,
  id: 'T2',
  source_row: 3,
  reference: 'NF 2',
  applied_principal: 0,
  received_amount: 0,
  open_amount: 10000,
  proposed_amount: 10000,
  status: 'review',
};
const doc: ReconciliationDocument = {
  version: 1,
  title: 'Conciliação de recebíveis',
  currency: 'BRL',
  period_start: '2026-07-01',
  period_end: '2026-07-31',
  receivables: [paid, proposed],
  bank_entries: [
    {
      row: 1,
      date: '2026-07-02',
      description: 'Recebimento confirmado',
      amount: 10000,
      kind: 'matched',
      proposed: false,
      matched_ids: ['T1'],
    },
  ],
  totals: {
    original_amount: 20000,
    duplicate_excluded: 0,
    received_confirmed: 10000,
    principal_confirmed: 10000,
    open_balance: 10000,
    proposed_amount: 10000,
    projected_open_balance: 0,
    unapplied_cash: 0,
    bank_fees: 0,
    fines_received: 0,
    withholding_pending: 0,
  },
};
const results: RunResults = {
  ...mockResults,
  demo: false,
  document: doc,
  findings: [
    {
      ...mockResults.findings[0]!,
      id: 'critical',
      severity: 'critical',
      title: 'Boleto precisa de conferência',
      amountAtRisk: 1,
      documentRows: [proposed],
    },
    {
      ...mockResults.findings[1]!,
      id: 'info',
      severity: 'info',
      title: 'Pagamento identificado',
      amountAtRisk: 100000,
    },
  ],
};
async function expandAll() {
  await userEvent.click(screen.getByText(t('results.all_heading')));
  await screen.findAllByTestId('finding-row');
}

describe('ResultsScreen', () => {
  it('shows the corrected download and critical warning without opening any rows', () => {
    render(<ResultsScreen results={results} onExport={vi.fn()} />);
    expect(screen.getByRole('button', { name: t('results.download_corrected') })).toBeEnabled();
    expect(
      screen.getByRole('button', {
        name: t('results.critical_open', { title: results.findings[0]!.title }),
      }),
    ).toBeVisible();
    expect(screen.queryByTestId('finding-row')).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByText('Cliente exemplo')).toBeNull();
  });
  it('puts critical findings before large informational amounts and exposes both ledgers on demand', async () => {
    render(<ResultsScreen results={results} onExport={vi.fn()} />);
    await expandAll();
    expect(
      screen
        .getAllByTestId('finding-row')
        .map((row) => within(row).getByRole('button').querySelector('span')?.textContent),
    ).toEqual(['Boleto precisa de conferência', 'Pagamento identificado']);
    expect(
      screen.getByRole('region', { name: t('results.doc_ledger') }).querySelectorAll('tbody tr'),
    ).toHaveLength(2);
    expect(
      screen.getByRole('region', { name: t('results.doc_bank') }).querySelectorAll('tbody tr'),
    ).toHaveLength(1);
  });
  it('keeps informational findings out of the pending review and distinguishes unapplied balances', async () => {
    render(<ResultsScreen results={results} onExport={vi.fn()} />);
    await userEvent.click(screen.getByText(t('results.review_heading')));
    expect(await screen.findAllByTestId('finding-row')).toHaveLength(1);
    expect(screen.queryByText('Pagamento identificado')).toBeNull();
    expect(
      screen.getByText(
        t('results.file_proposed_change', { amount: 'R$ 100,00', after: 'R$ 0,00' }),
        { exact: false },
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(t('results.file_pending_help'))).toBeInTheDocument();
  });
  it('records a review without changing the document or removing its critical warning', async () => {
    const onDecision = vi.fn();
    const before = JSON.stringify(doc);
    render(<ResultsScreen results={results} onDecision={onDecision} onExport={vi.fn()} />);
    const opener = screen.getByRole('button', {
      name: t('results.critical_open', { title: results.findings[0]!.title }),
    });
    await userEvent.click(opener);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText(t('results.file_pending'))).toBeInTheDocument();
    expect(within(dialog).getByText(t('results.review_scope'))).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole('button', { name: t('results.accept') }));
    expect(onDecision).toHaveBeenCalledWith('critical', 'accepted', undefined);
    expect(within(dialog).getByText(t('results.feedback_saved'))).toBeInTheDocument();
    expect(JSON.stringify(doc)).toBe(before);
    fireEvent(dialog, new Event('cancel', { cancelable: true }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(opener).toHaveFocus();
    expect(opener).toBeVisible();
  });
  it('keeps a failed review open and unchanged', async () => {
    render(
      <ResultsScreen
        results={results}
        onDecision={vi.fn().mockRejectedValue(new Error('Falha ao salvar.'))}
      />,
    );
    await userEvent.click(
      screen.getByRole('button', {
        name: t('results.critical_open', { title: results.findings[0]!.title }),
      }),
    );
    const dialog = screen.getByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: t('results.accept') }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Falha ao salvar.');
    expect(within(dialog).getByRole('button', { name: t('results.accept') })).toBeEnabled();
  });
  it('lets a read-only viewer inspect evidence without changing a finding', async () => {
    const onDecision = vi.fn();
    render(<ResultsScreen results={results} canDecide={false} onDecision={onDecision} />);
    await userEvent.click(
      screen.getByRole('button', {
        name: t('results.critical_open', { title: results.findings[0]!.title }),
      }),
    );
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText(t('results.file_pending'))).toBeVisible();
    for (const label of ['results.accept', 'results.edit'] as const)
      expect(within(dialog).queryByRole('button', { name: t(label) })).toBeNull();
    expect(onDecision).not.toHaveBeenCalled();
  });
  it('saves a review note with a clear scope', async () => {
    render(<ResultsScreen results={mockResults} />);
    await expandAll();
    await userEvent.click(within(screen.getAllByTestId('finding-row')[1]!).getByRole('button'));
    const dialog = screen.getByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: t('results.edit') }));
    const note = within(dialog).getByLabelText(t('results.edit_label'));
    expect(note).toHaveAttribute('maxlength');
    await userEvent.type(note, 'Conferido na origem');
    await userEvent.click(within(dialog).getByRole('button', { name: t('results.edit_save') }));
    expect(within(dialog).getByText('Conferido na origem')).toBeInTheDocument();
  });
  it('uses the live endpoint for every format and exposes failures inline', async () => {
    const onExport = vi.fn().mockResolvedValue(undefined);
    render(<ResultsScreen results={results} onExport={onExport} />);
    await userEvent.click(screen.getByRole('button', { name: t('results.download_corrected') }));
    expect(onExport).toHaveBeenLastCalledWith('xlsx');
    await userEvent.click(screen.getByRole('button', { name: t('results.download_report') }));
    expect(onExport).toHaveBeenLastCalledWith('pdf');
    await userEvent.click(screen.getByText(t('results.other_formats')));
    onExport.mockRejectedValueOnce(new Error('Falha ao gerar documento.'));
    await userEvent.click(screen.getByRole('button', { name: t('results.download_data_csv') }));
    expect(onExport).toHaveBeenLastCalledWith('csv');
    expect(await screen.findByRole('alert')).toHaveTextContent('Falha ao gerar documento.');
  });
  it('does not mislabel legacy findings as corrected data', async () => {
    render(<ResultsScreen results={{ ...mockResults, demo: false }} onExport={vi.fn()} />);
    expect(screen.queryByRole('button', { name: t('results.download_corrected') })).toBeNull();
    expect(screen.getByRole('link', { name: t('results.generate_corrected') })).toHaveAttribute(
      'href',
      '/app',
    );
    expect(screen.getByText(t('results.legacy_output'))).toBeInTheDocument();
    await userEvent.click(screen.getByText(t('results.other_formats')));
    expect(screen.getByRole('button', { name: t('results.download_findings_xlsx') })).toBeVisible();
  });
  it('downloads a clearly labeled findings CSV in the offline demo', async () => {
    const createObjectURL = vi.fn().mockReturnValue('blob:x');
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true });
    Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true });
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    render(<ResultsScreen results={mockResults} />);
    await userEvent.click(screen.getByText(t('results.other_formats')));
    await userEvent.click(screen.getByRole('button', { name: t('results.download_findings_csv') }));
    expect(click).toHaveBeenCalledTimes(1);
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    click.mockRestore();
  });
  it('builds a findings CSV and sorts without mutating its source', () => {
    expect(findingsCsv(mockResults.findings).split('\r\n').filter(Boolean)).toHaveLength(
      mockResults.findings.length + 1,
    );
    const source = [...results.findings].reverse();
    sortByRisk(source);
    expect(source[0]?.id).toBe('info');
  });
});

describe('MediaPlayer', () => {
  it('seeks to the cited timestamp once metadata is loaded', () => {
    render(
      <MediaPlayer
        evidence={{
          kind: 'audio',
          sourceName: 'reuniao.m4a',
          src: 'blob:demo',
          mimeType: 'audio/wav',
          timestamp: 75,
        }}
      />,
    );
    const media = screen.getByTestId<HTMLMediaElement>('evidence-media');
    media.dispatchEvent(new Event('loadedmetadata'));
    expect(media.currentTime).toBe(75);
  });
});

it('distinguishes an applied payment from its remaining withholding obligation', () => {
  render(
    <DocumentRowEffect
      row={{
        ...paid,
        status: 'withholding',
        applied_principal: 9000,
        received_amount: 9000,
        open_amount: 1000,
      }}
      currency="BRL"
    />,
  );
  expect(screen.getByText(t('results.file_partial_pending'))).toBeInTheDocument();
  expect(screen.queryByText(t('results.file_pending'))).toBeNull();
});
