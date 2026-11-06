import { ApiError, type RequestOptions } from '../../web/src/lib/apiTypes';
import { browser } from './browser';
import { t } from './i18n';
import type { APIRequest } from './policy';

interface Reply<T> {
  ok: boolean;
  value?: T;
  error?: { code: string; status?: number; details?: unknown };
}
function failure(code: string, status = 0, details?: unknown): ApiError {
  const message = ['session_changed', 'sign_in_required'].includes(code)
    ? t('extensions.sign_in_again')
    : status === 403 || status === 404
      ? t('projects.results_unavailable')
      : status === 409
        ? t('projects.conflict_help')
        : status
          ? t('errors.server', { status })
          : t('errors.network');
  return new ApiError(status, code, message, undefined, details);
}
export async function message<T>(type: string, request?: APIRequest): Promise<T> {
  const reply = (await browser.runtime.sendMessage({
    type,
    ...(request ? { request } : {}),
  })) as Reply<T>;
  if (!reply?.ok)
    throw failure(
      reply?.error?.code ?? 'connection_failed',
      reply?.error?.status,
      reply?.error?.details,
    );
  return reply.value as T;
}

function download(request: APIRequest, signal?: AbortSignal): Promise<Blob> {
  signal?.throwIfAborted();
  return new Promise((resolve, reject) => {
    const port = browser.runtime.connect({ name: 'docmatch.export' });
    const chunks: BlobPart[] = [];
    let mime = 'application/octet-stream';
    let settled = false;
    let sequence = 0;
    let timer: ReturnType<typeof setTimeout>;
    const finish = (error?: Error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal?.removeEventListener('abort', abort);
      port.disconnect();
      if (error) reject(error);
      else resolve(new Blob(chunks, { type: mime }));
    };
    const abort = () => finish(new DOMException('aborted', 'AbortError'));
    const reset = () => {
      clearTimeout(timer);
      timer = setTimeout(() => finish(failure('connection_failed')), 30000);
    };
    signal?.addEventListener('abort', abort, { once: true });
    port.onDisconnect.addListener(() => finish(failure('connection_failed')));
    port.onMessage.addListener((value) => {
      if (settled) return;
      reset();
      const m = value as {
        type?: string;
        mime?: string;
        data?: string;
        code?: string;
        status?: number;
        sequence?: number;
      };
      if (m.type === 'error') {
        finish(failure(m.code ?? 'connection_failed', m.status));
        return;
      }
      if (!Number.isSafeInteger(m.sequence) || m.sequence! < 0 || m.sequence! > sequence) {
        finish(failure('invalid_response'));
        return;
      }
      if (m.sequence! < sequence) return;
      if (m.type === 'ready' && typeof m.mime === 'string') {
        mime = m.mime;
        port.postMessage({ type: 'next', sequence: ++sequence });
      } else if (
        m.type === 'chunk' &&
        typeof m.data === 'string' &&
        m.data.length <= 44000 &&
        /^[A-Za-z0-9_-]*$/.test(m.data)
      ) {
        chunks.push(
          Uint8Array.from(atob(m.data.replaceAll('-', '+').replaceAll('_', '/')), (c) =>
            c.charCodeAt(0),
          ),
        );
        port.postMessage({ type: 'next', sequence: ++sequence });
      } else if (m.type === 'end') finish();
      else finish(failure(m.code ?? 'connection_failed', m.status));
    });
    reset();
    port.postMessage({ type: 'start', sequence, request });
  });
}

/** Every closure captures one identity and account. Later UI selections cannot redirect it. */
export function boundRequest(epoch: string, orgId?: string, onDenied?: (error: ApiError) => void) {
  return async <T>(path: string, opts: RequestOptions = {}): Promise<T> => {
    opts.signal?.throwIfAborted();
    if (opts.orgId && opts.orgId !== orgId) throw failure('account_changed', 403);
    const request: APIRequest = {
      sessionEpoch: epoch,
      path,
      ...(orgId ? { orgId } : {}),
      ...(opts.method ? { method: opts.method as NonNullable<APIRequest['method']> } : {}),
      ...(opts.body === undefined ? {} : { body: opts.body }),
      ...(opts.idempotencyKey || opts.idempotent
        ? { idempotencyKey: opts.idempotencyKey ?? crypto.randomUUID() }
        : {}),
    };
    try {
      if (opts.responseType === 'stream') throw failure('unsupported_request');
      const result =
        opts.responseType === 'blob'
          ? await download(request, opts.signal)
          : await message<T>('api', request);
      opts.signal?.throwIfAborted();
      return result as T;
    } catch (error) {
      if (error instanceof ApiError && [401, 403, 404].includes(error.status)) onDenied?.(error);
      throw error;
    }
  };
}
