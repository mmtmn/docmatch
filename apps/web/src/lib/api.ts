// Thin client for the docmatch API. Sessions live in HttpOnly cookies (never in web storage),
// so every call sends credentials; errors arrive as the {code, message, request_id} envelope and
// surface as ApiError with the server's pt-BR message.
import { apiUrl } from './env';
import { t } from './i18n';
import { selectedAccount } from './accountContext';

import { ApiError, type RequestOptions } from './apiTypes';
export { ApiError, type RequestOptions } from './apiTypes';

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
  const headers: Record<string, string> = {
    Accept: opts.responseType === 'stream' ? 'text/event-stream' : 'application/json',
  };
  if (opts.orgId && !path.startsWith('/v1/auth/')) headers['X-Docmatch-Org'] = opts.orgId;
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.body);
  }
  if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey;
  else if (opts.idempotent) headers['Idempotency-Key'] = newKey();
  let res: Response;
  try {
    res = await fetch(url, {
      method: opts.method ?? (body === undefined ? 'GET' : 'POST'),
      headers,
      ...(body === undefined ? {} : { body }),
      credentials: 'include',
      cache: 'no-store',
      ...(opts.signal ? { signal: opts.signal } : {}),
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, 'network', t('errors.network'));
  }
  if (res.status === 204) return undefined as T;
  if (res.ok && opts.responseType === 'blob') return (await res.blob()) as T;
  if (res.ok && opts.responseType === 'stream') return res.body as T;
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
let refreshing: Promise<unknown> | null = null;

export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  // Capture once: a later account switch must not redirect a retry or refresh.
  opts = {
    ...opts,
    orgId: opts.orgId === undefined ? (selectedAccount()?.orgId ?? null) : opts.orgId,
  };
  if (opts.idempotent && !opts.idempotencyKey) opts = { ...opts, idempotencyKey: newKey() };
  try {
    return await send<T>(path, opts);
  } catch (err) {
    if (
      !opts.noRefresh &&
      err instanceof ApiError &&
      err.status === 401 &&
      err.code === 'session_expired'
    ) {
      // Refresh tokens rotate. Concurrent refreshes would replay the old token and revoke
      // the session family, so all callers share one refresh before retrying their requests.
      refreshing ??= send<unknown>('/v1/auth/refresh', { method: 'POST', noRefresh: true }).finally(
        () => {
          refreshing = null;
        },
      );
      await refreshing;
      opts.signal?.throwIfAborted();
      return send<T>(path, opts);
    }
    throw err;
  }
}
