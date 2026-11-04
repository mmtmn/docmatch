import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { t } from '../lib/i18n';
import type { AcceptedFile, RejectedFile } from '../lib/precheck';
import { DropZone } from './DropZone';

function accepted(name: string, size: number, kind: AcceptedFile['kind'] = 'pdf'): AcceptedFile {
  return {
    id: `id-${name}`,
    name,
    size,
    kind,
    family: kind === 'audio' || kind === 'video' ? 'media' : 'document',
    file: new File([new Uint8Array(size)], name),
  };
}

const noop = () => {};

describe('DropZone', () => {
  it('shows the prompt and opens the file dialog on click', async () => {
    const onAdd = vi.fn();
    render(
      <DropZone
        files={[]}
        rejected={[]}
        dragging={false}
        onAdd={onAdd}
        onRemove={noop}
        onDismissRejection={noop}
      />,
    );
    const click = vi.spyOn(HTMLInputElement.prototype, 'click').mockImplementation(noop);
    await userEvent.click(screen.getByRole('button', { name: new RegExp(t('dropzone.title')) }));
    expect(click).toHaveBeenCalledTimes(1);
    click.mockRestore();
  });

  it('forwards files chosen in the dialog and resets the input', () => {
    const onAdd = vi.fn();
    const { container } = render(
      <DropZone
        files={[]}
        rejected={[]}
        dragging={false}
        onAdd={onAdd}
        onRemove={noop}
        onDismissRejection={noop}
      />,
    );
    const input = container.querySelector('input[type="file"]') as HTMLInputElement;
    expect(input.accept).toContain('.ofx');
    expect(input.accept).toContain('audio/*');
    const file = new File(['%PDF'], 'nota.pdf');
    fireEvent.change(input, { target: { files: [file] } });
    expect(onAdd).toHaveBeenCalledWith([file]);
  });

  it('lists chips with type, size and a remove button', async () => {
    const onRemove = vi.fn();
    render(
      <DropZone
        files={[accepted('extrato.ofx', 2048, 'ofx'), accepted('nota.pdf', 1024 * 1024)]}
        rejected={[]}
        dragging={false}
        onAdd={noop}
        onRemove={onRemove}
        onDismissRejection={noop}
      />,
    );
    const list = screen.getByRole('list', { name: t('dropzone.list_label') });
    expect(list.querySelectorAll('li')).toHaveLength(2);
    expect(screen.getByText(`${t('dropzone.kind_ofx')} · 2 KB`)).toBeInTheDocument();
    expect(screen.getByText(`${t('dropzone.kind_pdf')} · 1 MB`)).toBeInTheDocument();
    expect(screen.getByText(/2 arquivos/)).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole('button', { name: t('dropzone.remove_file', { name: 'nota.pdf' }) }),
    );
    expect(onRemove).toHaveBeenCalledWith('id-nota.pdf');
  });

  it('announces rejections and lets the user dismiss them', async () => {
    const onDismiss = vi.fn();
    const rejected: RejectedFile[] = [
      { id: 'r1', name: 'x.exe', size: 1, reason: 'motivo em português' },
    ];
    render(
      <DropZone
        files={[]}
        rejected={rejected}
        dragging={false}
        onAdd={noop}
        onRemove={noop}
        onDismissRejection={onDismiss}
      />,
    );
    expect(screen.getByText('motivo em português')).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole('button', { name: t('dropzone.dismiss_rejection', { name: 'x.exe' }) }),
    );
    expect(onDismiss).toHaveBeenCalledWith('r1');
  });

  it('exposes dragging and compact states as data attributes', () => {
    render(
      <DropZone
        files={[]}
        rejected={[]}
        dragging
        compact
        onAdd={noop}
        onRemove={noop}
        onDismissRejection={noop}
      />,
    );
    const zone = screen.getByRole('region', { name: t('dropzone.label') });
    expect(zone).toHaveAttribute('data-dragging', 'true');
    expect(zone).toHaveAttribute('data-compact', 'true');
  });
});
