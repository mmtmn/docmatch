import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const session = vi.hoisted(() => ({
  APP_PATH: '/app',
  authMethods: vi.fn(),
  currentUser: vi.fn(),
  devLogin: vi.fn(),
  listDevices: vi.fn(),
  logout: vi.fn(),
  revokeDevice: vi.fn(),
  signInLocally: vi.fn(),
  startAppleLogin: vi.fn(),
  startEmailLogin: vi.fn(),
  startGoogleLogin: vi.fn(),
  verifyEmailLogin: vi.fn(),
  verifyMagicLink: vi.fn(),
}));
vi.mock('../../lib/session', () => session);

const { AccountPage } = await import('./AccountPage');
const { noticeFor } = await import('./LoginPage');
const { MagicLink } = await import('./MagicLink');

const me = {
  user: { id: 'usr_1', email: 'ana@example.com', display_name: 'Ana' },
  org: { id: 'org_1', name: 'Ana' },
  credits: { balance: 10000, reserved: 0, available: 9960 },
};

const devices = [
  {
    id: 'ses_A',
    platform: 'web',
    name: 'Firefox · Linux',
    created_at: '2026-11-05T10:00:00Z',
    last_seen_at: '2026-11-05T10:05:00Z',
    current: true,
  },
  {
    id: 'ses_B',
    platform: 'android',
    name: 'App Android · Pixel 8',
    created_at: '2026-11-05T09:00:00Z',
    last_seen_at: '2026-11-05T09:30:00Z',
    current: false,
  },
  {
    id: 'ses_C',
    platform: 'ios',
    name: 'App iOS · iPhone',
    created_at: '2026-11-05T08:00:00Z',
    last_seen_at: '2026-11-05T08:30:00Z',
    current: false,
  },
];

describe('AccountPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    session.authMethods.mockResolvedValue({
      email: true,
      google: false,
      apple: false,
      password: true,
      dev: false,
    });
  });

  it('shows the account and every device signed in to it', async () => {
    session.currentUser.mockResolvedValue(me);
    session.listDevices.mockResolvedValue(devices);
    render(<AccountPage />);
    expect(await screen.findByText('ana@example.com')).toBeInTheDocument();
    expect(screen.getByText('9.960 tokens disponíveis')).toBeInTheDocument();
    for (const d of devices) expect(screen.getByText(d.name)).toBeInTheDocument();
    expect(screen.getByText('Este aparelho')).toBeInTheDocument();
  });

  it('signs another device out and reloads the list', async () => {
    session.currentUser.mockResolvedValue(me);
    session.listDevices.mockResolvedValueOnce(devices).mockResolvedValueOnce(devices.slice(0, 2));
    session.revokeDevice.mockResolvedValue(undefined);
    render(<AccountPage />);
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Encerrar a sessão de App iOS · iPhone' }));
    expect(session.revokeDevice).toHaveBeenCalledWith('ses_C');
    expect(await screen.findByText('Sessão encerrada.')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText('App iOS · iPhone')).toBeNull());
  });

  it('signs in locally when the API offers it and nobody is signed in', async () => {
    session.currentUser.mockResolvedValue(null);
    session.signInLocally.mockResolvedValue(me);
    session.listDevices.mockResolvedValue(devices.slice(0, 1));
    render(<AccountPage />);
    expect(await screen.findByText('ana@example.com')).toBeInTheDocument();
  });

  it('asks to sign in otherwise', async () => {
    session.currentUser.mockResolvedValue(null);
    session.signInLocally.mockResolvedValue(null);
    render(<AccountPage />);
    expect(await screen.findByRole('heading', { name: 'Entrar na docmatch' })).toBeInTheDocument();
  });

  it('signing out this device returns to the sign-in', async () => {
    session.currentUser.mockResolvedValue(me);
    session.listDevices.mockResolvedValue(devices);
    session.logout.mockResolvedValue(undefined);
    render(<AccountPage />);
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Sair deste aparelho' }));
    expect(session.logout).toHaveBeenCalled();
    expect(
      await screen.findByText('Você saiu. Entre de novo para ver sua conta.'),
    ).toBeInTheDocument();
  });
});

describe('LoginPage notices', () => {
  it('maps the OAuth error codes to pt-BR', () => {
    expect(noticeFor(null)).toBeNull();
    expect(noticeFor('oauth_state_mismatch')).toMatch(/expirou/);
    expect(noticeFor('algo_novo')).toBe('Não foi possível entrar. Tente de novo.');
  });
});

describe('MagicLink', () => {
  const location = window.location;
  afterEach(() => {
    Object.defineProperty(window, 'location', { configurable: true, value: location });
  });

  function at(search: string) {
    const assign = vi.fn();
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...location, search, assign },
    });
    return assign;
  }

  it('confirms the link and opens the app', async () => {
    const assign = at('?challenge_id=chl_1&code=tok');
    session.verifyMagicLink.mockResolvedValueOnce(me);
    render(<MagicLink />);
    await waitFor(() => expect(assign).toHaveBeenCalledWith('/app'));
    expect(session.verifyMagicLink).toHaveBeenCalledWith('chl_1', 'tok');
  });

  it('explains a broken link', async () => {
    at('?challenge_id=chl_1');
    render(<MagicLink />);
    expect(await screen.findByRole('alert')).toHaveTextContent('Não foi possível confirmar o link');
    expect(session.verifyMagicLink).not.toHaveBeenCalled();
  });
});
