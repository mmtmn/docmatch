import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

const session = vi.hoisted(() => ({
  startEmailLogin: vi.fn(),
  verifyEmailLogin: vi.fn(),
  startGoogleLogin: vi.fn(),
}));
vi.mock('../lib/session', () => session);

const { AuthDialog } = await import('./AuthDialog');

describe('AuthDialog', () => {
  it('signs in with the e-mail code', async () => {
    session.startEmailLogin.mockResolvedValueOnce({ challenge_id: 'chl_1', expires_at: '' });
    session.verifyEmailLogin.mockResolvedValueOnce({
      user: { id: 'usr_1', email: 'a@b.co', display_name: '' },
      org: { id: 'org_1', name: '' },
    });
    const onSignedIn = vi.fn();
    render(<AuthDialog open onClose={() => {}} onSignedIn={onSignedIn} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('E-mail'), 'a@b.co');
    await user.click(screen.getByRole('button', { name: 'Enviar código' }));
    expect(session.startEmailLogin).toHaveBeenCalledWith('a@b.co');
    await user.type(await screen.findByLabelText('Código de 6 dígitos'), '12a3456');
    await user.click(screen.getByRole('button', { name: 'Entrar' }));
    expect(session.verifyEmailLogin).toHaveBeenCalledWith('chl_1', '123456');
    expect(onSignedIn).toHaveBeenCalled();
  });

  it('refuses an invalid e-mail without calling the API', async () => {
    render(<AuthDialog open onClose={() => {}} onSignedIn={() => {}} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('E-mail'), 'nao-e-email');
    await user.click(screen.getByRole('button', { name: 'Enviar código' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Informe um e-mail válido.');
    expect(session.startEmailLogin).not.toHaveBeenCalledWith('nao-e-email');
  });
});
