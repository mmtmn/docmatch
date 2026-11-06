import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const ALL = { email: true, google: true, apple: true, password: true, dev: false };

const session = vi.hoisted(() => ({
  APP_PATH: '/app',
  authMethods: vi.fn(),
  devLogin: vi.fn(),
  passwordLogin: vi.fn(),
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
    await user.click(await screen.findByRole('button', { name: 'Receber um código por e-mail' }));
    await user.type(await screen.findByLabelText('E-mail'), 'a@b.co');
    await user.click(screen.getByRole('button', { name: 'Enviar código' }));
    expect(session.startEmailLogin).toHaveBeenCalledWith('a@b.co');
    await user.type(await screen.findByLabelText('Código de 6 dígitos'), '12a3456');
    await user.click(screen.getByRole('button', { name: 'Entrar' }));
    expect(session.verifyEmailLogin).toHaveBeenCalledWith('chl_1', '123456');
    expect(onSignedIn).toHaveBeenCalledWith(me);
  });

  it('signs in with a password and clears it after success', async () => {
    const onSignedIn = vi.fn();
    session.passwordLogin.mockResolvedValueOnce(me);
    render(<AuthDialog open inline onClose={() => {}} onSignedIn={onSignedIn} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Entrar com e-mail e senha' }));
    await user.type(screen.getByLabelText('E-mail'), 'a@b.co');
    const password = screen.getByLabelText('Senha');
    await user.type(password, 'fixture-password-only');
    expect(password).toHaveAttribute('type', 'password');
    expect(screen.queryByText(/autenticador/i)).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Mostrar senha' }));
    expect(password).toHaveAttribute('type', 'text');
    await user.click(screen.getByRole('button', { name: 'Ocultar senha' }));
    expect(password).toHaveAttribute('type', 'password');
    await user.click(screen.getByRole('button', { name: 'Entrar' }));
    expect(session.passwordLogin).toHaveBeenCalledWith('a@b.co', 'fixture-password-only');
    expect(onSignedIn).toHaveBeenCalledWith(me);
    expect(screen.getByLabelText('Senha')).toHaveValue('');
  });

  it('keeps invalid credentials on the login screen', async () => {
    session.passwordLogin.mockRejectedValueOnce(new Error('Credenciais inválidas.'));
    const onSignedIn = vi.fn();
    render(<AuthDialog open inline onClose={() => {}} onSignedIn={onSignedIn} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Entrar com e-mail e senha' }));
    await user.type(screen.getByLabelText('E-mail'), 'a@b.co');
    await user.type(screen.getByLabelText('Senha'), 'wrong-fixture-password');
    await user.click(screen.getByRole('button', { name: 'Entrar' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Credenciais inválidas.');
    expect(onSignedIn).not.toHaveBeenCalled();
  });

  it('refuses an invalid e-mail without calling the API', async () => {
    render(<AuthDialog open onClose={() => {}} onSignedIn={() => {}} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Receber um código por e-mail' }));
    await user.type(await screen.findByLabelText('E-mail'), 'nao-e-email');
    await user.click(screen.getByRole('button', { name: 'Enviar código' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Informe um e-mail válido.');
    expect(session.startEmailLogin).not.toHaveBeenCalled();
  });

  it('shows only the methods the API offers', async () => {
    session.authMethods.mockResolvedValue({ ...ALL, google: false, apple: false });
    render(<AuthDialog open onClose={() => {}} onSignedIn={() => {}} />);
    expect(
      await screen.findByRole('button', { name: 'Receber um código por e-mail' }),
    ).toBeInTheDocument();
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
      password: false,
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

  it('reopens with a fresh form and ignores a login that completed after dismissal', async () => {
    let finish!: (value: typeof me) => void;
    session.passwordLogin.mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    const onSignedIn = vi.fn();
    const props = { onClose: vi.fn(), onSignedIn, inline: true };
    const view = render(<AuthDialog {...props} open />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Entrar com e-mail e senha' }));
    await user.type(screen.getByLabelText('E-mail'), 'a@b.co');
    await user.type(screen.getByLabelText('Senha'), 'fixture-password');
    await user.click(screen.getByRole('button', { name: 'Entrar' }));
    view.rerender(<AuthDialog {...props} open={false} />);
    view.rerender(<AuthDialog {...props} open />);
    await act(async () => {
      finish(me);
      await Promise.resolve();
    });
    expect(onSignedIn).not.toHaveBeenCalled();
    expect(await screen.findByRole('button', { name: 'Entrar com Google' })).toBeEnabled();
    expect(screen.queryByLabelText('Senha')).toBeNull();
    expect(screen.queryByLabelText('E-mail')).toBeNull();
  });

  it('allows signing in again after returning from Google through the back-forward cache', async () => {
    session.startGoogleLogin.mockResolvedValue(undefined);
    render(<AuthDialog open inline onClose={vi.fn()} onSignedIn={vi.fn()} />);
    const user = userEvent.setup();
    const google = await screen.findByRole('button', { name: 'Entrar com Google' });
    await user.click(google);
    expect(google).toBeDisabled();
    act(() => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
    });
    expect(google).toBeEnabled();
    await user.click(google);
    expect(session.startGoogleLogin).toHaveBeenCalledTimes(2);
  });

  it('shows successful logout as a status, not an error', () => {
    render(
      <AuthDialog
        open
        inline
        onClose={vi.fn()}
        onSignedIn={vi.fn()}
        notice="Você saiu."
        noticeKind="status"
      />,
    );
    expect(screen.getByText('Você saiu.')).toHaveAttribute('role', 'status');
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('can retry loading the sign-in methods after an unavailable API', async () => {
    session.authMethods
      .mockResolvedValueOnce({
        email: false,
        google: false,
        apple: false,
        password: false,
        dev: false,
      })
      .mockResolvedValueOnce(ALL);
    render(<AuthDialog open inline onClose={vi.fn()} onSignedIn={vi.fn()} />);
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Tentar novamente' }));
    expect(await screen.findByRole('button', { name: 'Entrar com Google' })).toBeEnabled();
    expect(session.authMethods).toHaveBeenCalledTimes(2);
  });
});
