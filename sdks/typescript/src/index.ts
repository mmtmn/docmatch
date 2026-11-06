// Apache-2.0. No telemetry. Keep API keys in server-side environments.
import { ProjectClient, type Project, type Page } from './projects.js';
export * from './projects.js';
import { operations } from './operations.js';
export { operations };
export interface Quote {
  id: string;
  signature: string;
  expected_credits: number;
  cap_credits: number;
}
export interface Run {
  project_id?: string;
  id: string;
  status: 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'needs_approval';
  summary?: Record<string, unknown>;
  error?: { code: string; message: string };
}
export interface Upload {
  id: string;
  file_name: string;
  status: string;
  rejected_reason?: string;
}
export interface Finding {
  id: string;
  code: string;
  kind: string;
  title: string;
  severity: string;
  amount_at_risk: number;
  evidence: unknown[];
}
export interface Input {
  sources: { upload_id: string }[];
  rules_text?: string;
  options: {
    lane: 'api_batch' | 'api_standard' | 'api_priority';
    use_rules: boolean;
    web_search: boolean;
  };
}
export class DocmatchError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public requestId?: string,
    public details?: unknown,
  ) {
    super(message);
    this.name = 'DocmatchError';
  }
}
const safeURL = (value: string) => {
  const u = new URL(value);
  if (
    u.username ||
    u.password ||
    !(
      u.protocol === 'https:' ||
      (u.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(u.hostname))
    )
  )
    throw new Error('HTTPS required outside loopback');
  return u;
};
const pause = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    signal?.throwIfAborted();
    const abort = () => {
      clearTimeout(timer);
      reject(signal?.reason);
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', abort);
      resolve();
    }, ms);
    signal?.addEventListener('abort', abort, { once: true });
  });
const delay = (header: string | null, n: number) => {
  if (header !== null && /^\d+$/.test(header)) return Number(header) * 1000;
  const date = Date.parse(header ?? '');
  return Number.isFinite(date)
    ? Math.max(0, date - Date.now())
    : 250 * 2 ** n + Math.random() * 250;
};
export class Client {
  readonly baseURL: string;
  constructor(
    private options: {
      baseURL: string;
      apiKey: string;
      lane?: Input['options']['lane'];
      fetch?: typeof fetch;
      timeoutMs?: number;
    },
  ) {
    const u = safeURL(options.baseURL);
    if (u.search || u.hash) throw new Error('Expected API origin');
    this.baseURL = options.baseURL.replace(/\/$/, '');
  }
  project(id: string): ProjectClient { return new ProjectClient(this,id,this.options.lane ?? 'api_batch'); }
  listProjects({ archived = false, cursor = '', signal }: { archived?: boolean; cursor?: string; signal?: AbortSignal } = {}): Promise<Page<Project>> {
    return this.request('GET', operations.listProjects.path + `?archived=${archived}&cursor=${encodeURIComponent(cursor)}`, undefined, signal);
  }
  createProject(name: string, signal?: AbortSignal): Promise<Project> { return this.request('POST', operations.createProject.path, { name }, signal); }
  input(ids: string[], rulesText?: string): Input {
    return {
      sources: ids.map((upload_id) => ({ upload_id })),
      ...(rulesText ? { rules_text: rulesText } : {}),
      options: { lane: this.options.lane ?? 'api_batch', use_rules: true, web_search: false },
    };
  }
  private async response(
    method: string,
    path: string,
    body: unknown,
    signal?: AbortSignal,
  ): Promise<Response> {
    if (!path.startsWith('/v1/') || path.includes('\\'))
      throw new Error('Expected relative API path');
    const idempotencyKey = crypto.randomUUID();
    const retry = method === 'GET' || (method === 'POST' && (path === operations.createRun.path || /^\/v1\/projects\/[^/]+\/runs$/.test(path)));
    const requestSignal = AbortSignal.any([
      AbortSignal.timeout(this.options.timeoutMs ?? 120_000),
      ...(signal ? [signal] : []),
    ]);
    for (let attempt = 0; ; attempt++) {
      let res: Response;
      try {
        res = await (this.options.fetch ?? fetch)(this.baseURL + path, {
          method,
          redirect: 'error',
          credentials: 'omit',
          signal: requestSignal,
          headers: {
            Accept: 'application/json',
            Authorization: `Bearer ${this.options.apiKey}`,
            ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
            ...(method === 'GET' ? {} : { 'Idempotency-Key': idempotencyKey }),
          },
          ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        });
      } catch (e) {
        if (retry && attempt < 3 && !requestSignal.aborted) {
          await pause(delay(null, attempt), requestSignal);
          continue;
        }
        throw e;
      }
      if (res.ok) return res;
      if (attempt < 3 && (res.status === 429 || (retry && res.status >= 500))) {
        await res.body?.cancel();
        await pause(delay(res.headers.get('Retry-After'), attempt), requestSignal);
        continue;
      }
      const error = (await res.json().catch(() => ({}))) as {
        code?: string;
        message?: string;
        request_id?: string;
        details?: unknown;
      };
      throw new DocmatchError(
        res.status,
        error.code ?? 'http_error',
        error.message ?? `HTTP ${res.status}`,
        error.request_id,
        error.details,
      );
    }
  }
  async request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    const res = await this.response(method, path, body, signal);
    return res.status === 204 ? (undefined as T) : ((await res.json()) as T);
  }
  estimate(input: Input, signal?: AbortSignal): Promise<Quote> {
    return this.request('POST', operations.estimateRun.path, input, signal);
  }
  run(quote: Quote, input: Input, signal?: AbortSignal): Promise<Run> {
    return this.request(
      'POST',
      operations.createRun.path,
      { ...input, quote_id: quote.id, quote_signature: quote.signature },
      signal,
    );
  }
  getRun(id: string, signal?: AbortSignal): Promise<Run> {
    return this.request(
      'GET',
      operations.getRun.path.replace('{id}', encodeURIComponent(id)),
      undefined,
      signal,
    );
  }
  async wait(
    id: string,
    {
      signal,
      timeoutMs = 600_000,
      pollMs = 1000,
      onProgress,
      projectId,
    }: {
      signal?: AbortSignal;
      timeoutMs?: number;
      pollMs?: number;
      onProgress?: (run: Run) => void;
      projectId?: string;
    } = {},
  ): Promise<Run> {
    const bounded = AbortSignal.any([AbortSignal.timeout(timeoutMs), ...(signal ? [signal] : [])]);
    for (;;) {
      const run = await (projectId ? this.project(projectId).getRun(id, bounded) : this.getRun(id, bounded));
      onProgress?.(run);
      if (run.status === 'completed') return run;
      if (['failed', 'cancelled', 'needs_approval'].includes(run.status))
        throw new DocmatchError(
          409,
          run.error?.code ?? run.status,
          run.error?.message ?? 'Execução interrompida ou aguardando aprovação.',
        );
      await pause(pollMs, bounded);
    }
  }
  async findings(id: string, signal?: AbortSignal, projectId?: string): Promise<Finding[]> {
    const all: Finding[] = [];
    let cursor = '';
    for (let page = 0; page < 1000; page++) {
      const data = await this.request<{ items: Finding[]; next_cursor?: string }>(
        'GET',
        (projectId ? operations.projectListFindings.path.replace('{project}', encodeURIComponent(projectId)) : operations.listFindings.path).replace('{id}', encodeURIComponent(id)) +
          `?limit=100&cursor=${encodeURIComponent(cursor)}`,
        undefined,
        signal,
      );
      all.push(...data.items);
      if (!data.next_cursor) return all;
      if (data.next_cursor === cursor) throw new Error('Repeated cursor');
      cursor = data.next_cursor;
    }
    throw new Error('Pagination limit exceeded');
  }
  async export(
    id: string,
    format: 'xlsx' | 'csv' | 'pdf',
    signal?: AbortSignal,
    projectId?: string,
  ): Promise<Uint8Array> {
    if (!['xlsx', 'csv', 'pdf'].includes(format)) throw new Error('Invalid export format');
    return new Uint8Array(
      await (
        await this.response(
          'GET',
          (projectId ? operations.projectExportRun.path.replace('{project}', encodeURIComponent(projectId)) : operations.exportRun.path).replace('{id}', encodeURIComponent(id)) + '?format=' + format,
          undefined,
          signal,
        )
      ).arrayBuffer(),
    );
  }
  async upload(
    name: string,
    data: Uint8Array,
    mime = 'application/octet-stream',
    signal?: AbortSignal,
    destination?: { projectId: string; role?: string; replacesId?: string },
  ): Promise<Upload> {
    const prefix = destination ? operations.createProjectUpload.path.replace('{project}',encodeURIComponent(destination.projectId)) : operations.createUpload.path;
    const hash = await crypto.subtle.digest('SHA-256', new Uint8Array(data));
    const sha256 = Array.from(new Uint8Array(hash), (b) => b.toString(16).padStart(2, '0')).join(
      '',
    );
    const ticket = await this.request<{
      id: string;
      method: 'POST' | 'PUT';
      url: string;
      fields?: Record<string, string>;
      headers?: Record<string, string>;
    }>(
      'POST',
      prefix,
      { ...(destination?.role ? {role: destination.role} : {}), ...(destination?.replacesId ? {replaces_id: destination.replacesId} : {}), kind: 'document', file_name: name, mime, size_bytes: data.byteLength, sha256 },
      signal,
    );
    safeURL(ticket.url);
    let body: BodyInit;
    let headers: Record<string, string> | undefined;
    if (ticket.method === 'POST') {
      const form = new FormData();
      for (const [k, v] of Object.entries(ticket.fields ?? {})) form.set(k, v);
      form.set('file', new Blob([new Uint8Array(data)], { type: mime }), name);
      body = form;
    } else if (ticket.method === 'PUT') {
      body = new Uint8Array(data);
      headers = ticket.headers;
    } else throw new Error('Unsupported upload method');
    const res = await (this.options.fetch ?? fetch)(ticket.url, {
      method: ticket.method,
      body,
      headers,
      credentials: 'omit',
      redirect: 'error',
      signal: AbortSignal.any([AbortSignal.timeout(120_000), ...(signal ? [signal] : [])]),
    });
    if (!res.ok) throw new DocmatchError(res.status, 'upload_failed', 'Falha no envio do arquivo.');
    await res.body?.cancel();
    const path = `${prefix}/${encodeURIComponent(ticket.id)}/complete`;
    for (let polls = 0; polls < 120; polls++) {
      const upload = await this.request<Upload>('POST', path, { sha256 }, signal);
      if (upload.status === 'ready') return upload;
      if (!['pending', 'scanning'].includes(upload.status))
        throw new DocmatchError(422, 'upload_rejected', upload.rejected_reason ?? upload.status);
      await pause(1000, signal);
    }
    throw new Error('Upload scan timeout');
  }
}
export async function verifyWebhook(
  body: Uint8Array,
  header: string,
  secret: string,
  now = Date.now(),
): Promise<boolean> {
  if (!secret) return false;
  const parts = header.split(',').map((p) => p.trim().split('='));
  const timestamps = parts.filter(([k]) => k === 't');
  if (timestamps.length !== 1) return false;
  const timestamp = timestamps[0][1];
  if (!/^\d+$/.test(timestamp) || Math.abs(now / 1000 - Number(timestamp)) > 300) return false;
  const key = await crypto.subtle.importKey(
    'raw',
    new TextEncoder().encode(secret),
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['verify'],
  );
  const prefix = new TextEncoder().encode(timestamp + '.');
  const message = new Uint8Array(prefix.length + body.length);
  message.set(prefix);
  message.set(body, prefix.length);
  for (const [k, v] of parts)
    if (k === 'v1' && /^[a-f0-9]{64}$/i.test(v)) {
      const signature = Uint8Array.from(v.match(/../g)!, (b) => parseInt(b, 16));
      if (await crypto.subtle.verify('HMAC', key, signature, message)) return true;
    }
  return false;
}
