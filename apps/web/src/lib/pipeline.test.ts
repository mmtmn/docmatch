import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AcceptedFile } from './precheck';

vi.mock('./env', () => ({
  apiUrl: (path: string) => `https://api.invalid${path}`,
  apiBaseUrl: 'https://api.invalid',
}));

const { createRun, listFindings, uploadFile } = await import('./pipeline');

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function accepted(name: string, text: string): AcceptedFile {
  const file = new File([text], name, { type: 'text/csv' });
  return { id: name, file, name, size: file.size, kind: 'csv', family: 'document' };
}

describe('pipeline', () => {
  const fetchMock = vi.fn<typeof fetch>();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('uploads through the presigned form without cookies and waits for the scan', async () => {
    fetchMock
      .mockResolvedValueOnce(
        json(201, {
          id: 'upl_1',
          method: 'POST',
          url: 'https://s3.invalid/b',
          fields: { key: 'k', policy: 'p' },
        }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(json(202, { id: 'upl_1', file_name: 'a.csv', status: 'scanning' }))
      .mockResolvedValueOnce(json(200, { id: 'upl_1', file_name: 'a.csv', status: 'clean' }));
    const scanning = vi.fn();
    const done = uploadFile(accepted('a.csv', 'x;y\n1;2\n'), scanning);
    // Hashing resolves on a real event-loop turn, so wait for the scan wait to start first.
    await vi.waitFor(() => expect(scanning).toHaveBeenCalledWith('a.csv'));
    await vi.advanceTimersByTimeAsync(1000);
    await expect(done).resolves.toMatchObject({ id: 'upl_1', status: 'clean' });
    const create = JSON.parse(fetchMock.mock.calls[0]?.[1]?.body as string) as Record<
      string,
      unknown
    >;
    expect(create).toMatchObject({
      kind: 'document',
      file_name: 'a.csv',
      size_bytes: 8,
      mime: 'text/csv',
    });
    expect(create['sha256']).toMatch(/^[0-9a-f]{64}$/);
    const put = fetchMock.mock.calls[1];
    expect(put?.[0]).toBe('https://s3.invalid/b');
    expect(put?.[1]?.credentials).toBe('omit');
    const form = put?.[1]?.body as FormData;
    expect([...form.keys()]).toEqual(['key', 'policy', 'file']);
  });

  it('rejects a file the scanner refused', async () => {
    fetchMock
      .mockResolvedValueOnce(
        json(201, { id: 'upl_2', method: 'PUT', url: 'https://r2.invalid/k', headers: {} }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 200 }))
      .mockResolvedValueOnce(
        json(200, {
          id: 'upl_2',
          file_name: 'b.csv',
          status: 'rejected',
          rejected_reason: 'malware',
        }),
      );
    await expect(uploadFile(accepted('b.csv', 'x'))).rejects.toMatchObject({
      code: 'upload_rejected',
    });
  });

  it('reads every page of findings', async () => {
    fetchMock
      .mockResolvedValueOnce(json(200, { items: [{ id: 'f1' }], next_cursor: 'c2' }))
      .mockResolvedValueOnce(json(200, { items: [{ id: 'f2' }] }));
    const all = await listFindings('run_1');
    expect(all.map((f) => f.id)).toEqual(['f1', 'f2']);
    expect(fetchMock.mock.calls[1]?.[0] as string).toContain('cursor=c2');
  });

  it('starts the run with the signed quote and an idempotency key', async () => {
    fetchMock.mockResolvedValueOnce(json(202, { id: 'run_1', status: 'queued', sources: [] }));
    await createRun(
      {
        id: 'quo_1',
        expected_credits: 40,
        cap_credits: 50,
        signature: 'sig',
        requires_approval: false,
      },
      ['upl_1'],
      { rulesText: ' tarifa entra como despesa ', useRules: true, webSearch: false, ruleIds: [] },
    );
    const init = fetchMock.mock.calls[0]?.[1];
    const body = JSON.parse(init?.body as string) as Record<string, unknown>;
    expect(body).toMatchObject({
      quote_id: 'quo_1',
      quote_signature: 'sig',
      sources: [{ upload_id: 'upl_1' }],
      rules_text: 'tarifa entra como despesa',
    });
    expect((init?.headers as Record<string, string>)['Idempotency-Key']).toBeTruthy();
  });
});
