import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ApiError } from '../../web/src/lib/apiTypes';
const mocks = vi.hoisted(() => ({ send: vi.fn(), connect: vi.fn() }));
vi.mock('./browser', () => ({
  browser: { runtime: { sendMessage: mocks.send, connect: mocks.connect } },
}));
const { boundRequest } = await import('./client');
beforeEach(() => {
  mocks.send.mockReset();
  mocks.send.mockResolvedValue({ ok: true, value: { saved: true } });
});
describe('immutable extension destination', () => {
  it('keeps identity and account on every call and refuses a different caller-provided account', async () => {
    const first = boundRequest('first-epoch', 'org-first');
    const second = boundRequest('second-epoch', 'org-second');
    await first('/v1/projects', { orgId: 'org-first', body: { name: 'A' }, idempotent: true });
    await second('/v1/projects', { orgId: 'org-second' });
    expect(mocks.send.mock.calls[0]?.[0].request).toMatchObject({
      sessionEpoch: 'first-epoch',
      orgId: 'org-first',
      body: { name: 'A' },
    });
    expect(mocks.send.mock.calls[0]?.[0].request.idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(mocks.send.mock.calls[1]?.[0].request).toMatchObject({
      sessionEpoch: 'second-epoch',
      orgId: 'org-second',
    });
    await expect(first('/v1/projects', { orgId: 'org-second' })).rejects.toMatchObject({
      status: 403,
    });
    expect(mocks.send).toHaveBeenCalledTimes(2);
  });
  it('notifies the view of revoked access and honors an aborted operation before sending', async () => {
    const denied = vi.fn();
    const client = boundRequest('epoch', 'org', denied);
    mocks.send.mockResolvedValueOnce({ ok: false, error: { code: 'forbidden', status: 403 } });
    await expect(client('/v1/projects')).rejects.toBeInstanceOf(ApiError);
    expect(denied).toHaveBeenCalledOnce();
    await expect(client('/v1/projects', { signal: AbortSignal.abort() })).rejects.toMatchObject({
      name: 'AbortError',
    });
    expect(mocks.send).toHaveBeenCalledOnce();
  });
});

describe('download message delivery', () => {
  it('ignores duplicated replies without duplicating file bytes or requests', async () => {
    let receive: (message: unknown) => void = () => {};
    let disconnected: () => void = () => {};
    const postMessage = vi.fn();
    const disconnect = vi.fn(() => disconnected());
    mocks.connect.mockReturnValue({
      postMessage,
      disconnect,
      onMessage: {
        addListener: (fn: typeof receive) => {
          receive = fn;
        },
      },
      onDisconnect: {
        addListener: (fn: typeof disconnected) => {
          disconnected = fn;
        },
      },
    });
    const result = boundRequest('epoch', 'org')<Blob>('/export', { responseType: 'blob' });
    const ready = { type: 'ready', sequence: 0, mime: 'text/csv' };
    receive(ready);
    receive(ready);
    const chunk = { type: 'chunk', sequence: 1, data: btoa('one,two\n1,2\n').replaceAll('=', '') };
    receive(chunk);
    receive(chunk);
    receive({ type: 'end', sequence: 2 });
    expect(await (await result).text()).toBe('one,two\n1,2\n');
    expect(postMessage.mock.calls.map(([m]) => [m.type, m.sequence])).toEqual([
      ['start', 0],
      ['next', 1],
      ['next', 2],
    ]);
    expect(disconnect).toHaveBeenCalledOnce();
  });
});
