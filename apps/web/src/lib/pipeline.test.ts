import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AcceptedFile } from './precheck';

vi.mock('./env', () => ({
  apiUrl: (path: string) => `https://api.invalid${path}`,
  apiBaseUrl: 'https://api.invalid',
}));

const { confirmText, createRun, listFindings, uploadFile, watchRun } = await import('./pipeline');
const { selectAccount, clearAccountSelection } = await import('./accountContext');

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
    clearAccountSelection();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('reads SSE across split CRLF and UTF-8 chunks in the original account', async () => {
    let source!: ReadableStreamDefaultController<Uint8Array>;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        source = controller;
      },
    });
    fetchMock.mockResolvedValueOnce(
      new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } }),
    );
    selectAccount('user', 'org_original');
    const events = vi.fn();
    const close = watchRun('run_1', events);
    selectAccount('user', 'org_other');
    const message = new TextEncoder().encode(
      'event: progress\r\ndata: {"message":"Conciliação"}\r\n\r\n',
    );
    // Every byte in its own chunk exercises both line-ending and multibyte boundaries.
    for (const byte of message) source.enqueue(new Uint8Array([byte]));
    await vi.waitFor(() =>
      expect(events).toHaveBeenCalledWith({ event: 'progress', data: { message: 'Conciliação' } }),
    );
    expect(
      (fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>)['X-Docmatch-Org'],
    ).toBe('org_original');
    expect((fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>)['Accept']).toBe(
      'text/event-stream',
    );
    close();
    source.enqueue(new TextEncoder().encode('event: completed\ndata: {}\n\n'));
    source.close();
    await Promise.resolve();
    expect(events).toHaveBeenCalledTimes(1);
  });

  it('keeps project upload completion in its original destination after account selection changes', async () => {
    fetchMock
      .mockImplementationOnce(async () => {
        await Promise.resolve();
        selectAccount('user', 'org_other');
        return json(201, {
          id: 'upl_project',
          method: 'PUT',
          url: 'https://store.invalid/object',
          headers: {},
        });
      })
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(json(200, { id: 'upl_project', file_name: 'a.csv', status: 'ready' }));
    await uploadFile(accepted('a.csv', 'x;y\n1;2\n'), undefined, undefined, {
      orgId: 'org_original',
      projectId: 'prj_1',
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe('https://api.invalid/v1/projects/prj_1/uploads');
    expect(fetchMock.mock.calls[2]?.[0]).toBe(
      'https://api.invalid/v1/projects/prj_1/uploads/upl_project/complete',
    );
    expect(
      (fetchMock.mock.calls[2]?.[1]?.headers as Record<string, string>)['X-Docmatch-Org'],
    ).toBe('org_original');
  });

  it('uses an injected project transport and rejects an unregistered storage origin before sending bytes', async () => {
    const ticket = {
      id: 'upl_extension',
      method: 'PUT',
      url: 'https://storage.invalid/object',
      headers: {},
    };
    const request = vi
      .fn<typeof import('./api').api>()
      .mockResolvedValueOnce(ticket)
      .mockResolvedValueOnce({ id: ticket.id, status: 'ready', file_name: 'a.csv' });
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    const transport = {
      request: request as typeof import('./api').api,
      uploadOrigins: ['https://storage.invalid'],
    };
    const destination = { orgId: 'org_extension', projectId: 'prj_extension' };
    await uploadFile(accepted('a.csv', 'x;y\n1;2\n'), undefined, undefined, destination, transport);
    expect(request.mock.calls.map((call) => call[0])).toEqual([
      '/v1/projects/prj_extension/uploads',
      '/v1/projects/prj_extension/uploads/upl_extension/complete',
    ]);
    expect(request.mock.calls.every((call) => call[1]?.orgId === destination.orgId)).toBe(true);
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ credentials: 'omit', redirect: 'error' });
    request.mockResolvedValueOnce({ ...ticket, url: 'https://unregistered.invalid/object' });
    await expect(
      uploadFile(accepted('a.csv', 'x;y\n1;2\n'), undefined, undefined, destination, transport),
    ).rejects.toMatchObject({ code: 'upload_destination_invalid' });
    expect(fetchMock).toHaveBeenCalledOnce();
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
      .mockResolvedValueOnce(json(200, { id: 'upl_1', file_name: 'a.csv', status: 'ready' }));
    const scanning = vi.fn();
    const done = uploadFile(accepted('a.csv', 'x;y\n1;2\n'), scanning);
    // Hashing resolves on a real event-loop turn, so wait for the scan wait to start first.
    await vi.waitFor(() => expect(scanning).toHaveBeenCalledWith('a.csv'));
    await vi.advanceTimersByTimeAsync(1000);
    await expect(done).resolves.toMatchObject({ id: 'upl_1', status: 'ready' });
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

describe('confirmText', () => {
  const quote = {
    id: 'qte_1',
    expected_credits: 1234,
    cap_credits: 1500,
    signature: 'v1=x',
    requires_approval: false,
  };

  it('asks only for the tokens on a small order', () => {
    expect(confirmText(quote)).toBe(
      'Esta conciliação deve usar cerca de 1.234 tokens (no máximo 1.500). Continuar?',
    );
    expect(confirmText({ ...quote, expected_seconds: 40, large: false })).not.toContain(
      'Pedido grande',
    );
  });

  it('adds the expected duration of a large order', () => {
    expect(confirmText({ ...quote, expected_seconds: 4800, large: true })).toBe(
      'Esta conciliação deve usar cerca de 1.234 tokens (no máximo 1.500). Continuar?\n\n' +
        'Pedido grande: deve levar cerca de 1 h 20 min. Avisamos por e-mail quando terminar.',
    );
  });

  it('says nothing about time when the duration is missing', () => {
    expect(confirmText({ ...quote, large: true })).not.toContain('Pedido grande');
  });
});
