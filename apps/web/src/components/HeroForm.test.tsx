import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { Draft, DraftStore, LoadedDraft } from '../lib/draftStore';
import { t } from '../lib/i18n';
import { HeroForm, type RunRequest } from './HeroForm';

function memoryStore(
  initial: LoadedDraft | null = null,
  available = true,
): DraftStore & { saved: Draft[] } {
  let current = initial;
  const saved: Draft[] = [];
  return {
    saved,
    available: () => Promise.resolve(available),
    load: () => Promise.resolve(current),
    save: (draft) => {
      saved.push(draft);
      current = { ...draft, savedAt: 1 };
      return Promise.resolve({ ok: true, skipped: [] });
    },
    clear: () => {
      current = null;
      return Promise.resolve(true);
    },
  };
}

const pdf = (name: string, lastModified = 1): File =>
  new File(['%PDF-1.7 conteudo'], name, { type: 'application/pdf', lastModified });

function dropFiles(form: HTMLElement, files: File[]): void {
  fireEvent.drop(form, { dataTransfer: { files, types: ['Files'] } });
}

describe('HeroForm', () => {
  it('renders the five rows and keeps the button disabled until a file is added', async () => {
    render(<HeroForm store={memoryStore()} />);
    expect(screen.getByRole('heading', { level: 1, name: t('landing.title') })).toBeInTheDocument();
    expect(screen.getByText(t('landing.subtitle'))).toBeInTheDocument();
    expect(screen.getByRole('region', { name: t('dropzone.label') })).toBeInTheDocument();
    expect(screen.getByLabelText(t('form.rules_label'))).toBeInTheDocument();
    const button = screen.getByRole('button', { name: t('form.submit') });
    expect(button).toHaveAttribute('aria-disabled', 'true');
    expect(button).toHaveAccessibleDescription(t('form.disabled_no_files'));

    dropFiles(screen.getByTestId('hero'), [pdf('nota.pdf')]);
    await screen.findByText('nota.pdf');
    await waitFor(() => expect(button).not.toHaveAttribute('aria-disabled'));
  });

  it('rejects bad files with a reason and keeps the good ones', async () => {
    render(<HeroForm store={memoryStore()} />);
    dropFiles(screen.getByTestId('hero'), [
      pdf('nota.pdf'),
      new File([new Uint8Array([0, 1, 2])], 'x.exe'),
    ]);
    await screen.findByText('nota.pdf');
    expect(
      await screen.findByText(t('errors.file_type_unknown', { name: 'x.exe' })),
    ).toBeInTheDocument();
  });

  it('accepts pasted text as a file', async () => {
    render(<HeroForm store={memoryStore()} />);
    fireEvent.paste(document, { clipboardData: { files: [], getData: () => 'data;valor\n1;2' } });
    expect(await screen.findByText(t('dropzone.paste_name', { n: 1 }))).toBeInTheDocument();
  });

  it('restores a draft, lets the user discard it, and saves changes', async () => {
    const store = memoryStore({
      rulesText: 'regra antiga',
      useRules: false,
      webSearch: true,
      savedAt: 1,
      files: [
        {
          name: 'antigo.pdf',
          type: 'application/pdf',
          lastModified: 3,
          blob: new Blob(['%PDF-1.7']),
        },
      ],
    });
    render(<HeroForm store={store} />);
    expect(await screen.findByText(t('form.draft_restored'))).toBeInTheDocument();
    expect(await screen.findByText('antigo.pdf')).toBeInTheDocument();
    expect(screen.getByLabelText(t('form.rules_label'))).toHaveValue('regra antiga');
    expect(screen.getByRole('switch', { name: t('form.use_rules') })).toHaveAttribute(
      'aria-checked',
      'false',
    );
    expect(screen.getByRole('switch', { name: t('form.web_search') })).toHaveAttribute(
      'aria-checked',
      'true',
    );

    await userEvent.type(screen.getByLabelText(t('form.rules_label')), ' e nova');
    await waitFor(() => expect(store.saved.length).toBeGreaterThan(0), { timeout: 3000 });
    expect(store.saved.at(-1)?.rulesText).toBe('regra antiga e nova');
    expect(store.saved.at(-1)?.files.map((f) => f.name)).toEqual(['antigo.pdf']);

    await userEvent.click(screen.getByRole('button', { name: t('form.draft_clear') }));
    expect(await screen.findByText(t('form.draft_cleared'))).toBeInTheDocument();
    expect(screen.queryByText('antigo.pdf')).not.toBeInTheDocument();
    expect(screen.getByLabelText(t('form.rules_label'))).toHaveValue('');
  });

  it('asks the quote provider and submits the request', async () => {
    const quoteProvider = vi.fn().mockResolvedValue({ expected: 42, cap: 63 });
    const onSubmit = vi.fn<(request: RunRequest) => Promise<void>>().mockResolvedValue();
    render(
      <HeroForm
        store={memoryStore()}
        quoteProvider={quoteProvider}
        onSubmit={onSubmit}
        ruleOptions={[{ id: 'r1', title: 'Regra usada', usedBefore: true }]}
      />,
    );
    dropFiles(screen.getByTestId('hero'), [pdf('nota.pdf')]);
    await screen.findByText('nota.pdf');
    expect(
      await screen.findByText('≈ 42 tokens (máx. 63)', undefined, { timeout: 3000 }),
    ).toBeInTheDocument();
    expect(quoteProvider).toHaveBeenCalledWith(
      expect.objectContaining({ useRules: true, webSearch: false, selectedRules: ['r1'] }),
      expect.any(AbortSignal),
    );
    await userEvent.click(screen.getByRole('button', { name: t('form.submit') }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0]![0].files.map((f) => f.name)).toEqual(['nota.pdf']);
  });

  it('shows a pt-BR message when submission is not available yet', async () => {
    render(<HeroForm store={memoryStore()} />);
    dropFiles(screen.getByTestId('hero'), [pdf('nota.pdf')]);
    await screen.findByText('nota.pdf');
    await waitFor(() =>
      expect(screen.getByRole('button', { name: t('form.submit') })).not.toHaveAttribute(
        'aria-disabled',
      ),
    );
    await userEvent.click(screen.getByRole('button', { name: t('form.submit') }));
    expect(await screen.findByText(t('form.submit_unavailable'))).toBeInTheDocument();
  });

  it('works without storage', async () => {
    render(<HeroForm store={memoryStore(null, false)} />);
    dropFiles(screen.getByTestId('hero'), [pdf('nota.pdf')]);
    expect(await screen.findByText('nota.pdf')).toBeInTheDocument();
  });
});
