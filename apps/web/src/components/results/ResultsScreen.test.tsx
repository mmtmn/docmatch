import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { t } from '../../lib/i18n';
import { mockResults } from '../../lib/mock/results';
import { MediaPlayer } from './EvidencePanel';
import { findingsCsv, ResultsScreen, sortByRisk } from './ResultsScreen';

describe('ResultsScreen', () => {
  it('lists exceptions first, sorted by amount at risk, and collapses clean matches', async () => {
    render(<ResultsScreen results={mockResults} />);
    const rows = screen.getAllByTestId('finding-row');
    expect(rows).toHaveLength(mockResults.findings.length);
    const expected = sortByRisk(mockResults.findings).map((f) => f.title);
    expect(
      rows.map((row) => within(row).getByRole('button').querySelector('span')?.textContent),
    ).toEqual(expected);
    expect(
      screen.getByRole('heading', {
        level: 2,
        name: new RegExp(t('results.clean_summary', { count: 18 })),
      }),
    ).toBeInTheDocument();
    const toggle = screen.getByRole('button', { name: t('results.clean_show') });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await userEvent.click(toggle);
    expect(screen.getByRole('button', { name: t('results.clean_hide') })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    expect(document.getElementById('limpas-lista')).toBeInTheDocument();
  });

  it('opens the drawer with side-by-side evidence, records a decision and closes with Esc', async () => {
    const onDecision = vi.fn();
    render(<ResultsScreen results={mockResults} onDecision={onDecision} />);
    const first = sortByRisk(mockResults.findings)[0]!;
    const opener = within(screen.getAllByTestId('finding-row')[0]!).getByRole('button', {
      name: new RegExp(first.title),
    });
    await userEvent.click(opener);
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAttribute('aria-modal', 'true');
    expect(
      within(dialog).getByRole('heading', { level: 2, name: first.title }),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole('region', { name: first.left.sourceName })).toBeInTheDocument();
    expect(
      within(dialog).getByRole('region', { name: first.right.sourceName }),
    ).toBeInTheDocument();
    expect(dialog.querySelectorAll('[data-cited="true"]').length).toBeGreaterThan(0);

    await userEvent.click(within(dialog).getByRole('button', { name: t('results.accept') }));
    expect(onDecision).toHaveBeenCalledWith(first.id, 'accepted', undefined);
    expect(within(dialog).getByRole('button', { name: t('results.undo') })).toBeInTheDocument();
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
    expect(screen.getAllByTestId('finding-row')[0]).toHaveAttribute('data-status', 'accepted');
  });

  it('edit saves a note within the limit', async () => {
    render(<ResultsScreen results={mockResults} />);
    await userEvent.click(screen.getAllByTestId('finding-row')[1]!.querySelector('button')!);
    const dialog = screen.getByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: t('results.edit') }));
    const note = within(dialog).getByLabelText(t('results.edit_label'));
    expect(note).toHaveAttribute('maxlength');
    await userEvent.type(note, 'ISS retido');
    await userEvent.click(within(dialog).getByRole('button', { name: t('results.edit_save') }));
    expect(within(dialog).getByText('ISS retido')).toBeInTheDocument();
    expect(screen.getAllByTestId('finding-row')[1]).toHaveAttribute('data-status', 'edited');
  });

  it('exports CSV client-side and reports other formats as pending', async () => {
    const createObjectURL = vi.fn().mockReturnValue('blob:x');
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true });
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true });
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    render(<ResultsScreen results={mockResults} />);
    await userEvent.click(screen.getByRole('button', { name: t('results.export_csv') }));
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(click).toHaveBeenCalledTimes(1);
    expect(
      screen.getByText(t('results.export_done', { name: 'docmatch-demo.csv' })),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: t('results.export_pdf') }));
    expect(screen.getByText(t('results.export_soon', { format: 'PDF' }))).toBeInTheDocument();
    click.mockRestore();
  });

  it('builds a CSV with one line per finding', () => {
    const csv = findingsCsv(mockResults.findings);
    expect(csv.split('\r\n').filter(Boolean)).toHaveLength(mockResults.findings.length + 1);
    expect(csv).toContain(t('results.col_amount'));
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
    expect(screen.getByText(t('results.evidence_media_at', { time: '01:15' }))).toBeInTheDocument();
    media.dispatchEvent(new Event('loadedmetadata'));
    expect(media.currentTime).toBe(75);
  });
});
