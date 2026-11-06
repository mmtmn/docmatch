import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { t } from '../../lib/i18n';

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  preview: vi.fn(),
  respond: vi.fn(),
  logout: vi.fn(),
}));
vi.mock('../../lib/api', async () => ({
  ...(await vi.importActual('../../lib/api')),
  api: mocks.api,
}));
vi.mock('../../lib/teams', async () => ({
  ...(await vi.importActual('../../lib/teams')),
  previewInvitation: mocks.preview,
  respondInvitation: mocks.respond,
}));
vi.mock('../../lib/session', () => ({ logout: mocks.logout }));
vi.mock('../AuthDialog', () => ({ AuthDialog: () => null }));
import { InvitationPage } from './InvitationPage';

const preview = {
  id: 'inv_1',
  organization: 'Equipe Silva',
  email_hint: 'a***@example.test',
  role: 'uploader',
  status: 'pending',
  expires_at: '2026-11-13T12:00:00Z',
  email_matches: true,
};
describe('explicit invitation acceptance', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    window.history.replaceState(null, '', '/convite#token=an-invitation-token');
    mocks.api.mockResolvedValue({ user_id: 'usr_1', items: [] });
    mocks.preview.mockResolvedValue(preview);
  });
  it('opening the invitation previews it without granting access', async () => {
    render(<InvitationPage />);
    expect(await screen.findByRole('heading', { name: 'Equipe Silva' })).toBeVisible();
    expect(mocks.preview).toHaveBeenCalledWith('an-invitation-token', expect.any(AbortSignal));
    expect(mocks.respond).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: t('teams.accept_invitation') })).toBeEnabled();
  });
  it('requires the invited email and never offers acceptance to a different login', async () => {
    mocks.preview.mockResolvedValue({ ...preview, email_matches: false });
    mocks.logout.mockResolvedValue(undefined);
    render(<InvitationPage />);
    await screen.findByText(t('teams.invite_email_mismatch'));
    expect(screen.queryByRole('button', { name: t('teams.accept_invitation') })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: t('teams.switch_login') }));
    expect(mocks.logout).toHaveBeenCalledOnce();
    expect(mocks.respond).not.toHaveBeenCalled();
  });
  it('sends an explicit decline, removes the fragment, and does not create access', async () => {
    mocks.respond.mockResolvedValue({ organization_id: 'org_team', accepted: false });
    render(<InvitationPage />);
    await userEvent.click(
      await screen.findByRole('button', { name: t('teams.decline_invitation') }),
    );
    expect(mocks.respond).toHaveBeenCalledWith('an-invitation-token', false);
    await screen.findByText(t('teams.declined_message'));
    expect(window.location.hash).toBe('');
  });
  it('does not submit expired invitations', async () => {
    mocks.preview.mockResolvedValue({ ...preview, status: 'expired' });
    render(<InvitationPage />);
    await screen.findByText(t('teams.invite_expired'));
    expect(screen.queryByRole('button', { name: t('teams.accept_invitation') })).toBeNull();
    await waitFor(() => expect(mocks.respond).not.toHaveBeenCalled());
  });
});
