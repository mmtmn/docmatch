import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import { ApiKeysPanel } from './ApiKeysPanel';
import { api } from '../../lib/api';
vi.mock('../../lib/api', () => ({ api: vi.fn() }));
const mocked = vi.mocked(api);
beforeEach(() => mocked.mockReset());
it('shows paid eligibility and never renders a creation form for an unpaid account', async () => {
  mocked.mockResolvedValue({
    items: [],
    access: {
      can_create: false,
      eligible: false,
      limit: 1,
      lanes: ['api_batch'],
      scopes: ['runs:read'],
    },
  });
  render(<ApiKeysPanel />);
  expect(await screen.findByText(/Ative uma assinatura/)).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Criar chave' })).not.toBeInTheDocument();
});
it('shows a secret once, masks it, then clears it when acknowledged', async () => {
  const data = {
    items: [],
    access: {
      can_create: true,
      eligible: true,
      limit: 5,
      lanes: ['api_batch'],
      scopes: ['runs:read', 'runs:write', 'uploads:write'],
    },
  };
  mocked.mockImplementation((_path, opts) =>
    Promise.resolve(opts?.method === 'POST' ? { key: 'test-only-secret' } : data),
  );
  render(<ApiKeysPanel />);
  fireEvent.change(await screen.findByLabelText('Nome da integração'), {
    target: { value: 'Server' },
  });
  fireEvent.click(screen.getByRole('button', { name: 'Criar chave' }));
  const secret = await screen.findByLabelText(/Guarde esta chave/);
  expect(secret).toHaveAttribute('type', 'password');
  fireEvent.click(screen.getByRole('button', { name: 'Mostrar chave' }));
  expect(secret).toHaveAttribute('type', 'text');
  fireEvent.click(screen.getByRole('button', { name: 'Já guardei a chave' }));
  await waitFor(() =>
    expect(screen.queryByDisplayValue('test-only-secret')).not.toBeInTheDocument(),
  );
});
