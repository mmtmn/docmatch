import { act, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const session = vi.hoisted(() => ({
  APP_PATH: '/app',
  SESSION_CHANGED_EVENT: 'docmatch:session-changed',
  currentUser: vi.fn(),
}));
vi.mock('../../lib/session', () => session);
import { SessionNav } from './SessionNav';

const me = { user: { id: 'usr_test' } };

describe('SessionNav', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    session.currentUser.mockResolvedValue(null);
  });

  it('offers login to a signed-out visitor', async () => {
    render(<SessionNav path="/app" />);
    await waitFor(() => expect(session.currentUser).toHaveBeenCalled());
    expect(screen.getByRole('link', { name: 'Entrar' })).toHaveAttribute('href', '/login');
  });

  it('takes a signed-in app visitor straight to their account', async () => {
    session.currentUser.mockResolvedValue(me);
    render(<SessionNav path="/app" />);
    expect(await screen.findByRole('link', { name: 'Minha conta' })).toHaveAttribute(
      'href',
      '/conta',
    );
    expect(screen.queryByRole('link', { name: 'Entrar' })).toBeNull();
  });

  it.each(['/login', '/login/email'])('never links %s back to itself', (path) => {
    render(<SessionNav path={path} />);
    expect(screen.getByRole('link', { name: 'Abrir o app' })).toHaveAttribute('href', '/app');
    expect(session.currentUser).not.toHaveBeenCalled();
  });

  it('updates immediately after logout and subsequent login on the account page', async () => {
    session.currentUser.mockResolvedValue(me);
    render(<SessionNav path="/conta" />);
    expect(await screen.findByRole('link', { name: 'Abrir o app' })).toHaveAttribute(
      'href',
      '/app',
    );
    session.currentUser.mockResolvedValue(null);
    act(() => {
      window.dispatchEvent(new Event(session.SESSION_CHANGED_EVENT));
    });
    expect(await screen.findByRole('link', { name: 'Entrar' })).toHaveAttribute('href', '/login');
    session.currentUser.mockResolvedValue(me);
    act(() => {
      window.dispatchEvent(new Event(session.SESSION_CHANGED_EVENT));
    });
    expect(await screen.findByRole('link', { name: 'Abrir o app' })).toHaveAttribute(
      'href',
      '/app',
    );
  });

  it('ignores an old signed-in response that finishes after logout', async () => {
    let finish!: (value: typeof me) => void;
    session.currentUser.mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    render(<SessionNav path="/app" />);
    session.currentUser.mockResolvedValue(null);
    act(() => {
      window.dispatchEvent(new Event(session.SESSION_CHANGED_EVENT));
    });
    await act(async () => {
      finish(me);
      await Promise.resolve();
    });
    expect(screen.getByRole('link', { name: 'Entrar' })).toHaveAttribute('href', '/login');
    expect(screen.queryByRole('link', { name: 'Minha conta' })).toBeNull();
  });

  it('rechecks the session when a browser restores a cached page', async () => {
    session.currentUser.mockResolvedValue(me);
    render(<SessionNav path="/app" />);
    await screen.findByRole('link', { name: 'Minha conta' });
    session.currentUser.mockResolvedValue(null);
    act(() => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
    });
    expect(await screen.findByRole('link', { name: 'Entrar' })).toHaveAttribute('href', '/login');
  });
});
