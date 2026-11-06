import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { MessageSender } from './browser';
type Listener = (
  message: unknown,
  sender: MessageSender,
  respond: (response: unknown) => void,
) => boolean;
const mocks = vi.hoisted(() => ({
  data: {} as Record<string, unknown>,
  listener: null as Listener | null,
  fetch: vi.fn(),
  launch: vi.fn(),
  access: vi.fn(),
}));
vi.mock('./config', () => ({
  config: {
    clientID: 'chrome-local',
    apiURL: 'https://api.example.invalid',
    siteURL: 'https://site.example.invalid',
    browser: 'chrome',
    local: false,
  },
}));
vi.mock('./browser', () => ({
  browser: {
    runtime: {
      id: 'example',
      getURL: (p: string) => `chrome-extension://example/${p}`,
      onMessage: {
        addListener: (listener: Listener) => {
          mocks.listener = listener;
        },
      },
    },
    storage: {
      local: {
        get: async (key: string) => ({ [key]: structuredClone(mocks.data[key]) }),
        set: async (value: Record<string, unknown>) => {
          Object.assign(mocks.data, structuredClone(value));
        },
        remove: async (key: string) => {
          delete mocks.data[key];
        },
        setAccessLevel: mocks.access,
      },
    },
    identity: {
      getRedirectURL: () => 'https://extension.example.invalid/callback',
      launchWebAuthFlow: mocks.launch,
    },
  },
}));
const key = 'docmatch.session.chrome-local';
const epoch = '00000000-0000-4000-8000-000000000001';
const sender = { id: 'example', url: 'chrome-extension://example/workspace.html' };
const pair = (suffix = 'a', expired = false) => ({
  access_token: `dmxA1_00000000-0000-4000-8000-000000000001.${suffix.repeat(43)}`,
  refresh_token: `dmxR1_00000000-0000-4000-8000-000000000001.${suffix.repeat(43)}`,
  expires_at: new Date(Date.now() + (expired ? -1000 : 600000)).toISOString(),
  refresh_expires_at: new Date(Date.now() + 86400000).toISOString(),
});
async function send(
  message: unknown,
): Promise<{ ok: boolean; value?: unknown; error?: { code: string } }> {
  return new Promise((resolve, reject) => {
    if (!mocks.listener?.(message, sender, (response) => resolve(response as { ok: boolean })))
      reject(new Error('untrusted sender'));
  });
}
const api = () =>
  send({
    type: 'api',
    request: { sessionEpoch: epoch, path: '/v1/projects', orgId: 'org_00000000000000000000000000' },
  });
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });
beforeEach(async () => {
  vi.resetModules();
  vi.clearAllMocks();
  mocks.data = {};
  mocks.listener = null;
  mocks.access.mockResolvedValue(undefined);
  vi.stubGlobal('fetch', mocks.fetch);
  await import('./background');
});
describe('extension session lifecycle', () => {
  it('rejects website messages and stale identity context before network access', async () => {
    mocks.data[key] = { epoch: '00000000-0000-4000-8000-000000000002', tokens: pair() };
    expect(
      mocks.listener?.(
        { type: 'status' },
        { id: 'example', url: 'https://evil.example.invalid' },
        () => {},
      ),
    ).toBe(false);
    expect((await api()).error?.code).toBe('session_changed');
    expect(mocks.fetch).not.toHaveBeenCalled();
    expect(mocks.access).toHaveBeenCalledWith({ accessLevel: 'TRUSTED_CONTEXTS' });
  });
  it('refreshes once for concurrent requests and never exposes credentials in status', async () => {
    mocks.data[key] = { epoch, tokens: pair('a', true) };
    const issued = pair('b');
    let rotations = 0;
    mocks.fetch.mockImplementation(async (url: URL, options: RequestInit) => {
      expect(options.credentials).toBe('omit');
      if (url.pathname === '/v1/extensions/token') {
        rotations++;
        return response(issued);
      }
      return response({ items: [] });
    });
    const results = await Promise.all([api(), api()]);
    expect(results.every((r) => r.ok)).toBe(true);
    expect(rotations).toBe(1);
    const status = await send({ type: 'status' });
    expect(status.value).toEqual({ signedIn: true, epoch });
    expect(JSON.stringify(status)).not.toContain(issued.access_token);
  });
  it('repeats the persisted refresh request after a lost response and worker restart', async () => {
    mocks.data[key] = { epoch, tokens: pair('a', true) };
    const attempts: string[] = [];
    const issued = pair('b');
    mocks.fetch.mockImplementation(async (url: URL, options: RequestInit) => {
      if (url.pathname === '/v1/extensions/token') {
        attempts.push(JSON.parse(String(options.body)).request_id);
        if (attempts.length === 1) throw new TypeError('connection lost after server rotation');
        return response(issued);
      }
      return response({ items: [] });
    });
    expect((await api()).ok).toBe(false);
    vi.resetModules();
    await import('./background');
    expect((await api()).ok).toBe(true);
    expect(attempts).toHaveLength(2);
    expect(attempts[1]).toBe(attempts[0]);
  });
  it('clears revoked and expired credentials instead of keeping a signed-in cache', async () => {
    mocks.data[key] = { epoch, tokens: pair('a', true) };
    mocks.fetch.mockResolvedValue(response({ code: 'unauthenticated' }, 401));
    expect((await api()).ok).toBe(false);
    expect((await send({ type: 'status' })).value).toMatchObject({ signedIn: false });
    expect(JSON.stringify(mocks.data)).not.toContain('refresh_token');
  });
  it('does not let late authorization restore a signed-out session', async () => {
    mocks.data[key] = { epoch };
    let finish: (url: string) => void = () => {};
    mocks.launch.mockImplementation(
      () =>
        new Promise<string>((resolve) => {
          finish = resolve;
        }),
    );
    mocks.fetch.mockImplementation(async (url: URL) =>
      url.pathname.endsWith('/token') ? response(pair('c')) : new Response(null, { status: 204 }),
    );
    const pending = send({ type: 'signIn' });
    await vi.waitFor(() => expect(mocks.launch).toHaveBeenCalledOnce());
    await send({ type: 'signOut' });
    const start = new URL(mocks.launch.mock.calls[0]![0].url);
    finish(
      `https://extension.example.invalid/callback?state=${start.searchParams.get('state')}&code=${'d'.repeat(43)}`,
    );
    expect((await pending).ok).toBe(false);
    expect((await send({ type: 'status' })).value).toMatchObject({ signedIn: false });
    expect(
      mocks.fetch.mock.calls.some((args) => (args[0] as URL).pathname === '/v1/extensions/revoke'),
    ).toBe(true);
  });
});
