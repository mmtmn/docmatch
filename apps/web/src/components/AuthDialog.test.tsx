import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const ALL = { email: true, google: true, apple: true, password: true, dev: false };

const session = vi.hoisted(() => ({
  APP_PATH: '/app',
  authMethods: vi.fn(),
  devLogin: vi.fn(),
  startEmailLogin: vi.fn(),
  verifyEmailLogin: vi.fn(),
  startGoogleLogin: vi.fn(),
  startAppleLogin: vi.fn(),
}));
vi.mock('../lib/session', () => session);

const { AuthDialog } = await import('./AuthDialog');

const me = {
  user: { id: 'usr_1', email: 'a@b.co', display_name: '' },
  org: { id: 'org_1', name: '' },
};

describe('AuthDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    session.authMethods.mockResolvedValue(ALL);
  });

  it('signs in with the e-mail code', async () => {
    session.startEmailLogin.mockResolvedValueOnce({ challenge_id: 'chl_1', expires_at: '' });
    session.verifyEmailLogin.mockResolvedValueOnce(me);
    const onSignedIn = vi.fn();
    render(<AuthDialog open onClose={() => {}} onSignedIn={onSignedIn} />);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('E-mail'), 'a@b.co');
    await user.click(screen.getByRole('button', { name: 'Enviar código' }));
    expect(session.startEmailLogin).toHaveBeenCalledWith('a@b.co');
    await user.type(await screen.findByLabelText('Código de 6 dígitos'), '12a3456');
    await user.click(screen.getByRole('button', { name: 'Entrar' }));
    expect(session.verifyEmailLogin).toHaveBeenCalledWith('chl_1', '123456');
    expect(onSignedIn).toHaveBeenCalledWith(me);
  });

  it('refuses an invalid e-mail without calling the API', async () => {
    render(<AuthDialog open onClose={() => {}} onSignedIn={() => {}} />);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('E-mail'), 'nao-e-email');
    await user.click(screen.getByRole('button', { name: 'Enviar código' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Informe um e-mail válido.');
    expect(session.startEmailLogin).not.toHaveBeenCalled();
  });

  it('shows only the methods the API offers', async () => {
    session.authMethods.mockResolvedValue({ ...ALL, google: false, apple: false });
    render(<AuthDialog open onClose={() => {}} onSignedIn={() => {}} />);
    expect(await screen.findByLabelText('E-mail')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Entrar com Google' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Entrar com Apple' })).toBeNull();
  });

  it('offers the local sign-in on a development API', async () => {
    session.authMethods.mockResolvedValue({
      email: false,
      google: false,
      apple: false,
      password: true,
      dev: true,
    });
    session.devLogin.mockResolvedValueOnce(me);
    const onSignedIn = vi.fn();
    render(<AuthDialog open onClose={() => {}} onSignedIn={onSignedIn} />);
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Entrar sem conta (ambiente local)' }));
    expect(session.devLogin).toHaveBeenCalled();
    expect(onSignedIn).toHaveBeenCalledWith(me);
    expect(screen.queryByLabelText('E-mail')).toBeNull();
  });

  it('says so when no sign-in is available yet', async () => {
    session.authMethods.mockResolvedValue({
      email: false,
      google: false,
      apple: false,
      password: true,
      dev: false,
    });
    render(<AuthDialog open onClose={() => {}} onSignedIn={() => {}} />);
    expect(await screen.findByText(/O login ainda não está disponível/)).toBeInTheDocument();
  });

  it('brings an OAuth error back as a notice, inline', () => {
    render(
      <AuthDialog open inline onClose={() => {}} onSignedIn={() => {}} notice="O login expirou." />,
    );
    expect(screen.getByRole('alert')).toHaveTextContent('O login expirou.');
    expect(screen.getByRole('heading', { name: 'Entrar na docmatch' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Fechar' })).toBeNull();
  });
});
