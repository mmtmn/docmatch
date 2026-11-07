import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { t } from '../../lib/i18n';
import { SESSION_CHANGED_EVENT } from '../../lib/session';
const mocks = vi.hoisted(() => ({ pending: vi.fn(), respond: vi.fn() }));
vi.mock('../../lib/teams', async () => ({
  ...(await vi.importActual('../../lib/teams')),
  pendingInvitations: mocks.pending,
  respondPendingInvitation: mocks.respond,
}));
vi.mock('../AuthDialog', () => ({ AuthDialog: () => null }));
import { InvitationInboxPage } from './InvitationInboxPage';
const invitation = {
  id: 'inv_a',
  organization: 'Escritório A',
  email_hint: 'a***@test.example',
  role: 'uploader',
  status: 'pending',
  expires_at: '2026-11-13T12:00:00Z',
  email_matches: true,
};
describe('pending invitation inbox', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    mocks.pending.mockResolvedValue({ items: [invitation] });
  });
  it('shows permissions and requires a separate confirmation to join', async () => {
    render(<InvitationInboxPage />);
    await screen.findByRole('heading', { name: invitation.organization });
    expect(screen.getByText(t('teams.upload_explanation'))).toBeVisible();
    expect(mocks.respond).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: t('teams.accept_invitation') }));
    expect(mocks.respond).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: t('common.cancel') }));
    expect(mocks.respond).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: t('teams.accept_invitation') }));
    mocks.respond.mockResolvedValue({ organization_id: 'org_company', accepted: true });
    mocks.pending.mockResolvedValue({ items: [] });
    await userEvent.click(screen.getByRole('button', { name: t('common.confirm') }));
    expect(mocks.respond).toHaveBeenCalledExactlyOnceWith('inv_a', true);
    await screen.findByText(t('teams.inbox_accepted'));
    expect(window.location.pathname).not.toContain('account=');
  });
  it('loads the next page and sends an explicit decline', async () => {
    mocks.pending
      .mockResolvedValueOnce({ items: [invitation], next_cursor: 'inv_a' })
      .mockResolvedValueOnce({ items: [{ ...invitation, id: 'inv_b', organization: 'Equipe B' }] });
    render(<InvitationInboxPage />);
    await userEvent.click(await screen.findByRole('button', { name: t('teams.inbox_more') }));
    expect(mocks.pending).toHaveBeenLastCalledWith('inv_a', expect.any(AbortSignal));
    await screen.findByRole('heading', { name: 'Equipe B' });
    await userEvent.click(
      screen.getAllByRole('button', { name: t('teams.decline_invitation') })[0]!,
    );
    mocks.respond.mockResolvedValue({ organization_id: 'org_a', accepted: false });
    mocks.pending.mockResolvedValue({ items: [] });
    await userEvent.click(screen.getByRole('button', { name: t('common.confirm') }));
    expect(mocks.respond).toHaveBeenCalledWith('inv_a', false);
    await screen.findByText(t('teams.declined_message'));
  });
  it('clears an open confirmation when the login changes and ignores an old response', async () => {
    render(<InvitationInboxPage />);
    await userEvent.click(
      await screen.findByRole('button', { name: t('teams.accept_invitation') }),
    );
    mocks.pending.mockResolvedValue({ items: [] });
    void act(() => {
      window.dispatchEvent(new Event(SESSION_CHANGED_EVENT));
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(mocks.respond).not.toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: invitation.organization })).toBeNull();
  });
});
