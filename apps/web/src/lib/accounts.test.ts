import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('./env', () => ({
  apiUrl: (path: string) => `https://api.invalid${path}`,
  apiBaseUrl: 'https://api.invalid',
}));

const user = `usr_${'1'.repeat(26)}`;
const personal = `org_${'1'.repeat(26)}`;
const company = `org_${'2'.repeat(26)}`;
const otherUser = `usr_${'2'.repeat(26)}`;
const request = vi.fn<typeof fetch>();

function memberships(userId = user, includeCompany = true) {
  request.mockResolvedValueOnce(
    new Response(
      JSON.stringify({
        user_id: userId,
        items: [
          { id: personal, kind: 'personal' },
          ...(includeCompany ? [{ id: company, kind: 'team' }] : []),
        ],
      }),
      { headers: { 'Content-Type': 'application/json' } },
    ),
  );
}

async function tab() {
  vi.resetModules();
  return { ...(await import('./accountContext')), ...(await import('./accounts')) };
}

describe('remembered account destinations', () => {
  beforeEach(() => {
    window.sessionStorage.clear();
    window.localStorage.clear();
    window.history.replaceState(null, '', '/projetos');
    request.mockReset();
    vi.stubGlobal('fetch', request);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('restores a closed tab only after verifying its identity and current memberships', async () => {
    (await tab()).selectAccount(user, company);
    window.sessionStorage.clear();
    const reopened = await tab();
    expect(reopened.selectedAccount()).toBeNull();
    memberships();
    await reopened.accounts();
    expect(reopened.selectedAccount()?.orgId).toBe(company);
    expect(new Headers(request.mock.calls[0]?.[1]?.headers).has('X-Docmatch-Org')).toBe(false);
  });

  it('leaves an existing tab in its own account after another tab changes the remembered choice', async () => {
    const first = await tab();
    first.selectAccount(user, company);
    const second = await tab();
    second.selectAccount(user, personal);
    window.dispatchEvent(new StorageEvent('storage', { key: 'docmatch:account:last:v1' }));
    memberships();
    await first.accounts();
    expect(first.selectedAccount()?.orgId).toBe(company);
    expect(second.selectedAccount()?.orgId).toBe(personal);
  });

  it.each(['removed membership', 'different identity'])(
    'rejects a remembered choice for %s',
    async (reason) => {
      (await tab()).selectAccount(user, company);
      window.sessionStorage.clear();
      const reopened = await tab();
      memberships(
        reason === 'different identity' ? otherUser : user,
        reason !== 'removed membership',
      );
      await reopened.accounts();
      expect(reopened.selectedAccount()?.orgId).toBe(personal);
    },
  );

  it('clears the remembered choice on sign-out and ignores damaged storage', async () => {
    const current = await tab();
    current.selectAccount(user, company);
    current.clearAccountSelection();
    expect(current.rememberedAccount(user)).toBeNull();
    window.localStorage.setItem('docmatch:account:last:v1', '{broken');
    memberships();
    await current.accounts();
    expect(current.selectedAccount()?.orgId).toBe(personal);
  });

  it('honors a valid linked account before the remembered account', async () => {
    (await tab()).selectAccount(user, company);
    window.sessionStorage.clear();
    window.history.replaceState(null, '', `/projetos?account=${personal}`);
    const reopened = await tab();
    memberships();
    await reopened.accounts();
    expect(reopened.selectedAccount()?.orgId).toBe(personal);
  });
});
