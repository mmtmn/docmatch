import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
vi.mock('./env', () => ({
  apiUrl: (path: string) => `https://api.invalid${path}`,
  apiBaseUrl: 'https://api.invalid',
}));
import { currentUser, revokeDevice } from './session';
import { clearAccountSelection, selectAccount, selectedAccount } from './accountContext';

const json = (value: unknown) =>
  new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } });
describe('identity and selected account', () => {
  const request = vi.fn<typeof fetch>();
  beforeEach(() => {
    request.mockReset();
    vi.stubGlobal('fetch', request);
    clearAccountSelection();
    window.history.replaceState(null, '', '/conta');
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    clearAccountSelection();
  });
  it('recovers from a revoked company to the verified personal membership before loading profile', async () => {
    selectAccount('usr_1', 'org_removed');
    request
      .mockResolvedValueOnce(
        json({
          user_id: 'usr_1',
          items: [{ id: 'org_personal', kind: 'personal', name: 'Ana', role: 'owner' }],
        }),
      )
      .mockResolvedValueOnce(json({ user: { id: 'usr_1' }, org: { id: 'org_personal' } }));
    expect((await currentUser())?.org.id).toBe('org_personal');
    expect(
      (request.mock.calls[0]?.[1]?.headers as Record<string, string>)['X-Docmatch-Org'],
    ).toBeUndefined();
    expect((request.mock.calls[1]?.[1]?.headers as Record<string, string>)['X-Docmatch-Org']).toBe(
      'org_personal',
    );
  });
  it('revoking another device keeps this tab in the same company', async () => {
    selectAccount('usr_1', 'org_team');
    request.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await revokeDevice('ses_other');
    expect(selectedAccount()?.orgId).toBe('org_team');
  });
});
