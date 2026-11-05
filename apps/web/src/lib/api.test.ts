import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('./env', () => ({
  apiUrl: (path: string) => `https://api.invalid${path}`,
  apiBaseUrl: 'https://api.invalid',
}));

const { api, ApiError } = await import('./api');

function json(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('api', () => {
  const fetchMock = vi.fn<typeof fetch>();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('sends cookies and JSON, and returns the body', async () => {
    fetchMock.mockResolvedValueOnce(json(200, { ok: true }));
    await expect(api('/v1/me')).resolves.toEqual({ ok: true });
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe('https://api.invalid/v1/me');
    expect(init?.credentials).toBe('include');
    expect(init?.method).toBe('GET');
  });

  it('posts bodies with an idempotency key when asked', async () => {
    fetchMock.mockResolvedValueOnce(json(201, { id: 'run_1' }));
    await api('/v1/runs', { body: { a: 1 }, idempotent: true });
    const init = fetchMock.mock.calls[0]?.[1];
    const headers = init?.headers as Record<string, string>;
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe('{"a":1}');
    expect(headers['Content-Type']).toBe('application/json');
    expect(headers['Idempotency-Key']).toMatch(/.{8,}/);
  });

  it('turns the error envelope into ApiError with the server message', async () => {
    fetchMock.mockResolvedValueOnce(
      json(402, { code: 'insufficient_credits', message: 'Saldo insuficiente.', request_id: 'r1' }),
    );
    const err = await api('/v1/runs', { body: {} }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      status: 402,
      code: 'insufficient_credits',
      message: 'Saldo insuficiente.',
      requestId: 'r1',
    });
  });

  it('reports network failures in pt-BR', async () => {
    fetchMock.mockRejectedValueOnce(new TypeError('Failed to fetch'));
    await expect(api('/v1/me')).rejects.toMatchObject({ code: 'network' });
  });

  it('refreshes an expired access window once and retries', async () => {
    fetchMock
      .mockResolvedValueOnce(json(401, { code: 'session_expired', message: 'Sessão expirada.' }))
      .mockResolvedValueOnce(json(200, { ok: 1 }))
      .mockResolvedValueOnce(json(200, { user: { id: 'usr_1' } }));
    await expect(api('/v1/me')).resolves.toEqual({ user: { id: 'usr_1' } });
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual([
      'https://api.invalid/v1/me',
      'https://api.invalid/v1/auth/refresh',
      'https://api.invalid/v1/me',
    ]);
  });

  it('returns undefined for 204', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await expect(api('/v1/auth/logout', { method: 'POST' })).resolves.toBeUndefined();
  });
});
