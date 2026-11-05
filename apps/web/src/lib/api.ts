// Thin client for the docmatch API. Sessions live in HttpOnly cookies (never in web storage),
// so every call sends credentials; errors arrive as the {code, message, request_id} envelope and
// surface as ApiError with the server's pt-BR message.
import { apiUrl } from './env';
import { t } from './i18n';

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | undefined;
  readonly details: unknown;

  constructor(
    status: number,
    code: string,
    message: string,
    requestId?: string,
    details?: unknown,
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.details = details;
  }
}

export interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** Adds an Idempotency-Key, required by the endpoints that spend tokens. */
  idempotent?: boolean;
  /** Skip the silent refresh on an expired access window (used by the refresh itself). */
  noRefresh?: boolean;
}

interface Envelope {
  code?: string;
  message?: string;
  request_id?: string;
  details?: unknown;
}

/** True when the build knows where the API is. */
export function apiConfigured(): boolean {
  return apiUrl('/') !== undefined;
}

function newKey(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

async function send<T>(path: string, opts: RequestOptions): Promise<T> {
  const url = apiUrl(path);
  if (!url) throw new ApiError(0, 'api_unconfigured', t('results.live_unconfigured'));
  const headers: Record<string, string> = { Accept: 'application/json' };
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.body);
  }
  if (opts.idempotent) headers['Idempotency-Key'] = newKey();
  let res: Response;
  try {
    res = await fetch(url, {
      method: opts.method ?? (body === undefined ? 'GET' : 'POST'),
      headers,
      ...(body === undefined ? {} : { body }),
      credentials: 'include',
      ...(opts.signal ? { signal: opts.signal } : {}),
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, 'network', t('errors.network'));
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (!res.ok) {
    const env = (data ?? {}) as Envelope;
    throw new ApiError(
      res.status,
      env.code ?? 'server',
      env.message ?? t('errors.server', { status: res.status }),
      env.request_id,
      env.details,
    );
  }
  return data as T;
}

/** Calls the API; on an expired access window refreshes the session once and retries. */
export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  try {
    return await send<T>(path, opts);
  } catch (err) {
    if (
      !opts.noRefresh &&
      err instanceof ApiError &&
      err.status === 401 &&
      err.code === 'session_expired'
    ) {
      await send<unknown>('/v1/auth/refresh', { method: 'POST', noRefresh: true });
      return send<T>(path, opts);
    }
    throw err;
  }
}
