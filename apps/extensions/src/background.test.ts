import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { MessageSender, MessagePort } from './browser';
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
  native: vi.fn(),
  browserKind: 'chrome',
  portListener: null as ((port: MessagePort) => void) | null,
}));
vi.mock('./config', () => ({
  config: {
    clientID: 'chrome-local',
    apiURL: 'https://api.example.invalid',
    siteURL: 'https://site.example.invalid',
    get browser() {
      return mocks.browserKind;
    },
    redirectURI: 'docmatch-extension-dev://auth',
    local: false,
  },
}));
vi.mock('./browser', () => ({
  browser: {
    runtime: {
      sendNativeMessage: mocks.native,
      id: 'example',
      getURL: (p: string) => `chrome-extension://example/${p}`,
      onConnect: {
        addListener: (listener: (port: MessagePort) => void) => {
          mocks.portListener = listener;
        },
      },
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
  mocks.browserKind = 'chrome';
  mocks.access.mockResolvedValue(undefined);
  vi.stubGlobal('fetch', mocks.fetch);
  await import('./background');
});
describe('extension session lifecycle', () => {
  it('resumes Safari consent after restart without exposing the verifier or tokens to either UI', async () => {
    mocks.browserKind = 'safari';
    const id = '00000000-0000-4000-8000-000000000003';
    const openURL = `docmatch-extension-dev://authorize?request=${id}`;
    mocks.native.mockImplementation(async (_: string, body: Record<string, string>) => {
      if (body.action === 'begin') return { id, openURL };
      if (body.action === 'cancel') return { status: 'cancelled' };
      const pending = mocks.data['docmatch.authorization.chrome-local'] as { state: string };
      return { status: 'ready', state: pending.state, code: 'z'.repeat(43) };
    });
    expect((await send({ type: 'signIn' })).value).toEqual({ signedIn: false, openURL });
    const pending = mocks.data['docmatch.authorization.chrome-local'] as { verifier: string };
    expect(JSON.stringify(mocks.native.mock.calls)).not.toContain(pending.verifier);
    vi.resetModules();
    await import('./background');
    mocks.fetch.mockImplementation(async (_: URL, options: RequestInit) => {
      expect(JSON.parse(String(options.body)).code_verifier).toBe(pending.verifier);
      return response(pair());
    });
    const status = await send({ type: 'status' });
    expect(status.value).toMatchObject({ signedIn: true });
    expect(JSON.stringify(status)).not.toMatch(/dmx[AR]1_|verifier/);
    expect(JSON.stringify(mocks.native.mock.calls)).not.toContain('dmx');
    expect(mocks.data['docmatch.authorization.chrome-local']).toBeUndefined();
  });
  it('rejects Safari callbacks for another state and clears cancelled handoffs', async () => {
    mocks.browserKind = 'safari';
    const id = '00000000-0000-4000-8000-000000000003';
    mocks.native.mockImplementation(async (_: string, body: Record<string, string>) =>
      body.action === 'begin'
        ? { id, openURL: `docmatch-extension-dev://authorize?request=${id}` }
        : { status: 'ready', state: 'wrong', code: 'z'.repeat(43) },
    );
    await send({ type: 'signIn' });
    expect((await send({ type: 'status' })).ok).toBe(false);
    expect(mocks.fetch).not.toHaveBeenCalled();
    expect(mocks.data['docmatch.authorization.chrome-local']).toBeUndefined();
    await send({ type: 'signIn' });
    await send({ type: 'signOut' });
    expect(mocks.native).toHaveBeenLastCalledWith('docmatch', { action: 'cancel', id });
    expect((await send({ type: 'status' })).value).toMatchObject({ signedIn: false });
  });
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

function exportPort(from = sender) {
  const output: { type?: string; data?: string; code?: string }[] = [];
  let receive: (message: unknown) => void = () => {};
  let disconnect: () => void = () => {};
  const port = {
    name: 'docmatch.export',
    sender: from,
    postMessage: (message: unknown) => output.push(message as (typeof output)[number]),
    disconnect: vi.fn(() => disconnect()),
    onMessage: {
      addListener: (listener: typeof receive) => {
        receive = listener;
      },
    },
    onDisconnect: {
      addListener: (listener: typeof disconnect) => {
        disconnect = listener;
      },
    },
  };
  mocks.portListener?.(port);
  return { port, output, send: (value: unknown) => receive(value) };
}
const exportRequest = {
  sessionEpoch: epoch,
  orgId: 'org_00000000000000000000000000',
  path: '/v1/projects/prj_00000000000000000000000000/runs/run_00000000000000000000000000/export?format=csv',
};
describe('foreground exports', () => {
  it('streams each bounded chunk once despite duplicate messages and keeps credentials private', async () => {
    mocks.data[key] = { epoch, tokens: pair() };
    const data = new Uint8Array(100000).map((_, i) => i % 251);
    mocks.fetch.mockResolvedValue(new Response(data, { headers: { 'Content-Type': 'text/csv' } }));
    const stream = exportPort();
    stream.send({ type: 'start', sequence: 0, request: exportRequest });
    await vi.waitFor(() => expect(stream.output[0]?.type).toBe('ready'));
    stream.send({ type: 'start', sequence: 0, request: exportRequest });
    const decoded: number[] = [];
    for (let i = 0; i < 5; i++) {
      stream.send({ type: 'next', sequence: i + 1 });
      stream.send({ type: 'next', sequence: i + 1 });
      await vi.waitFor(() => expect(stream.output.length).toBe(i + 2));
      const item = stream.output[i + 1]!;
      if (item.type === 'end') break;
      const chunk = Uint8Array.from(
        atob(item.data!.replaceAll('-', '+').replaceAll('_', '/')),
        (c) => c.charCodeAt(0),
      );
      expect(chunk.length).toBeLessThanOrEqual(32768);
      decoded.push(...chunk);
    }
    expect(stream.output.at(-1)?.type).toBe('end');
    expect(stream.port.disconnect).not.toHaveBeenCalled();
    expect(new Uint8Array(decoded)).toEqual(data);
    expect(mocks.fetch).toHaveBeenCalledOnce();
    expect(JSON.stringify(stream.output)).not.toContain('dmxA1_');
    expect(mocks.fetch.mock.calls[0]![1]).toMatchObject({ credentials: 'omit', redirect: 'error' });
    stream.port.disconnect();
    expect(mocks.fetch.mock.calls[0]![1].signal.aborted).toBe(true);
  });
  it('rejects untrusted pages and cancels an old identity before sending file bytes', async () => {
    const foreign = exportPort({ id: 'example', url: 'https://example.invalid' });
    expect(foreign.port.disconnect).toHaveBeenCalled();
    mocks.data[key] = { epoch, tokens: pair() };
    mocks.fetch.mockResolvedValue(new Response('private file'));
    const stream = exportPort();
    stream.send({ type: 'start', sequence: 0, request: exportRequest });
    await vi.waitFor(() => expect(stream.output[0]?.type).toBe('ready'));
    mocks.data[key] = { epoch: '00000000-0000-4000-8000-000000000002', tokens: pair('b') };
    stream.send({ type: 'next', sequence: 1 });
    await vi.waitFor(() => expect(stream.output.at(-1)?.code).toBe('session_changed'));
    expect(stream.output.some((m) => m.type === 'chunk')).toBe(false);
    expect(stream.port.disconnect).not.toHaveBeenCalled();
    stream.port.disconnect();
  });
});
