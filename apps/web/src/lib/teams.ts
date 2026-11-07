import { api, ApiError } from './api';
import type { AccountFeatures, Capabilities, Organization } from './accounts';
import { t, type MessageKey } from './i18n';

export type SeatRole = 'uploader' | 'reconciler';
export interface TeamMember {
  id: string;
  name: string;
  email: string;
  role: Organization['role'];
  joined_at: string;
}
export interface Invitation {
  id: string;
  email: string;
  role: SeatRole;
  status: 'pending' | 'accepted' | 'declined' | 'expired' | 'revoked';
  expires_at: string;
  sent_at: string;
}
export interface Team {
  id: string;
  name: string;
  kind: Organization['kind'];
  role: Organization['role'];
  capabilities: Capabilities;
  members: TeamMember[];
  invitations: Invitation[];
  member_limit: number;
  seat_price: number;
  can_invite: boolean;
  features?: AccountFeatures;
}
export interface InvitationPreview {
  id: string;
  organization: string;
  email_hint: string;
  role: SeatRole;
  status: Invitation['status'];
  expires_at: string;
  email_matches: boolean;
}

const reasons: Record<string, MessageKey> = {
  invalid_email: 'teams.invalid_email',
  invalid_role: 'teams.invalid_role',
  team_required: 'teams.team_required',
  already_member: 'teams.already_member',
  member_limit: 'teams.member_limit',
  invite_rate_limited: 'teams.invite_rate_limited',
  invite_used: 'teams.invite_used',
  invite_expired: 'teams.invite_expired',
  invite_email_mismatch: 'teams.invite_email_mismatch',
  owner_transfer_required: 'teams.owner_transfer_required',
  same_owner: 'teams.same_owner',
  full_access_required: 'teams.full_access_required',
};
export function teamError(cause: unknown): string {
  if (
    cause instanceof ApiError &&
    cause.details &&
    typeof cause.details === 'object' &&
    'reason' in cause.details &&
    typeof cause.details.reason === 'string'
  ) {
    const key = reasons[cause.details.reason];
    if (key) return t(key);
  }
  return cause instanceof Error ? cause.message : t('errors.unknown');
}
export function teamClient(orgId: string) {
  return {
    get: (signal?: AbortSignal) => api<Team>('/v1/team', { orgId, ...(signal ? { signal } : {}) }),
    convert: (name: string) => api<Team>('/v1/team', { orgId, body: { name } }),
    invite: (email: string, role: SeatRole) =>
      api<Invitation>('/v1/team/invitations', { orgId, body: { email, role } }),
    resend: (id: string) =>
      api<void>(`/v1/team/invitations/${encodeURIComponent(id)}/resend`, { orgId, method: 'POST' }),
    revoke: (id: string) =>
      api<void>(`/v1/team/invitations/${encodeURIComponent(id)}`, { orgId, method: 'DELETE' }),
    changeRole: (id: string, role: SeatRole) =>
      api<void>(`/v1/team/members/${encodeURIComponent(id)}`, {
        orgId,
        method: 'PATCH',
        body: { role },
      }),
    remove: (id: string) =>
      api<void>(`/v1/team/members/${encodeURIComponent(id)}`, { orgId, method: 'DELETE' }),
    transfer: (userId: string) => api<void>('/v1/team/owner', { orgId, body: { user_id: userId } }),
    leave: () => api<void>('/v1/team/leave', { orgId, method: 'POST' }),
  };
}
export function previewInvitation(token: string, signal?: AbortSignal) {
  return api<InvitationPreview>('/v1/invitations/preview', {
    orgId: null,
    body: { token },
    ...(signal ? { signal } : {}),
  });
}
export function respondInvitation(token: string, accept: boolean) {
  return api<{ organization_id: string; accepted: boolean }>('/v1/invitations/respond', {
    orgId: null,
    body: { token, accept },
  });
}

export interface InvitationInbox {
  items: InvitationPreview[];
  next_cursor?: string;
}
export function pendingInvitations(cursor?: string, signal?: AbortSignal) {
  return api<InvitationInbox>(
    `/v1/me/invitations${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`,
    { orgId: null, ...(signal ? { signal } : {}) },
  );
}
export function respondPendingInvitation(id: string, accept: boolean) {
  return api<{ organization_id: string; accepted: boolean }>(
    `/v1/me/invitations/${encodeURIComponent(id)}/respond`,
    { orgId: null, body: { accept } },
  );
}
