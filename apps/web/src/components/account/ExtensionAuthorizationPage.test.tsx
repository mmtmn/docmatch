import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { t } from '../../lib/i18n';
const mocks = vi.hoisted(() => ({
  client: vi.fn(),
  authorize: vi.fn(),
  user: vi.fn(),
  logout: vi.fn(),
}));
vi.mock('../../lib/extensionAuthorization', async () => ({
  ...(await vi.importActual('../../lib/extensionAuthorization')),
  extensionClient: mocks.client,
  authorizeExtension: mocks.authorize,
}));
vi.mock('../../lib/session', () => ({ currentUser: mocks.user, logout: mocks.logout }));
vi.mock('../AuthDialog', () => ({ AuthDialog: () => null }));
import { ExtensionAuthorizationPage } from './ExtensionAuthorizationPage';
const params = new URLSearchParams({
  client_id: 'chrome-dev',
  redirect_uri: 'https://extension.example.invalid/',
  code_challenge: 'a'.repeat(43),
  code_challenge_method: 'S256',
  state: 'b'.repeat(43),
});
describe('extension consent', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    window.history.replaceState(null, '', `/extensoes/autorizar?${params}`);
    mocks.client.mockResolvedValue({ id: 'chrome-dev', name: 'Chrome local' });
    mocks.user.mockResolvedValue({ user: { email: 'ana@example.test' } });
  });
  it('does not authorize on opening, even when already signed in', async () => {
    render(<ExtensionAuthorizationPage />);
    expect(
      await screen.findByText(t('extensions.signed_in', { email: 'ana@example.test' })),
    ).toBeVisible();
    expect(mocks.authorize).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: t('extensions.allow') })).toBeEnabled();
  });
  it('submits only after explicit approval and preserves errors', async () => {
    mocks.authorize.mockRejectedValue(new Error('Conexão interrompida'));
    render(<ExtensionAuthorizationPage />);
    await userEvent.click(await screen.findByRole('button', { name: t('extensions.allow') }));
    expect(mocks.authorize).toHaveBeenCalledOnce();
    expect(mocks.authorize).toHaveBeenCalledWith(Object.fromEntries(params));
    expect(await screen.findByRole('alert')).toHaveTextContent('Conexão interrompida');
  });
  it('requires sign-in and a registered client', async () => {
    mocks.user.mockResolvedValue(null);
    render(<ExtensionAuthorizationPage />);
    expect(await screen.findByRole('button', { name: t('nav.login') })).toBeEnabled();
    expect(screen.queryByRole('button', { name: t('extensions.allow') })).toBeNull();
    expect(mocks.authorize).not.toHaveBeenCalled();
  });
  it('rejects invalid and repeated parameters before loading a session', async () => {
    window.history.replaceState(null, '', `/extensoes/autorizar?${params}&state=second`);
    render(<ExtensionAuthorizationPage />);
    expect(screen.getByRole('alert')).toHaveTextContent(t('extensions.invalid_request'));
    await waitFor(() => expect(mocks.client).not.toHaveBeenCalled());
    expect(mocks.user).not.toHaveBeenCalled();
  });
});
