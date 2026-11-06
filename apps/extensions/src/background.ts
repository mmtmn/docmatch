import { browser } from './browser';
import { config } from './config';
import {
  callbackCode,
  pkceChallenge,
  randomSecret,
  trustedPage,
  validAPIRequest,
  validExportRequest,
  base64url,
  type APIRequest,
} from './policy';

interface Tokens {
  access_token: string;
  refresh_token: string;
  expires_at: string;
  refresh_expires_at: string;
}
interface State {
  epoch: string;
  tokens?: Tokens;
  refreshRequestID?: string;
}
interface Pending {
  epoch: string;
  verifier: string;
  state: string;
  redirect: string;
  startedAt: number;
  nativeID?: string;
}
const storageKey = `docmatch.session.${config.clientID}`;
const pendingKey = `docmatch.authorization.${config.clientID}`;
let queue: Promise<unknown> = Promise.resolve();
function serialized<T>(fn: () => Promise<T>): Promise<T> {
  const next = queue.then(fn, fn);
  queue = next.catch(() => undefined);
  return next;
}
async function state(): Promise<State> {
  const stored = (await browser.storage.local.get(storageKey))[storageKey] as State | undefined;
  if (stored?.epoch) return stored;
  const initial = { epoch: crypto.randomUUID() };
  await save(initial);
  return initial;
}
function save(value: State) {
  return browser.storage.local.set({ [storageKey]: value });
}
const ready =
  browser.storage.local.setAccessLevel?.({ accessLevel: 'TRUSTED_CONTEXTS' }) ?? Promise.resolve();
class Failure extends Error {
  constructor(
    readonly code: string,
    readonly status = 0,
    readonly details?: unknown,
  ) {
    super(code);
  }
}
async function tokenRequest(body: Record<string, unknown>): Promise<Tokens> {
  const response = await fetch(new URL('/v1/extensions/token', config.apiURL), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'omit',
    cache: 'no-store',
    redirect: 'error',
    signal: AbortSignal.timeout(30000),
    body: JSON.stringify({ ...body, client_id: config.clientID }),
  });
  const data: unknown = await response.json();
  if (!response.ok) throw new Failure('sign_in_required', response.status);
  const t = data as Tokens;
  if (
    !/^dmxA1_[0-9a-f-]{36}\.[A-Za-z0-9_-]{43}$/.test(t.access_token) ||
    !/^dmxR1_[0-9a-f-]{36}\.[A-Za-z0-9_-]{43}$/.test(t.refresh_token) ||
    !Number.isFinite(Date.parse(t.expires_at)) ||
    !Number.isFinite(Date.parse(t.refresh_expires_at))
  )
    throw new Failure('invalid_response');
  return t;
}
let signingIn: Promise<unknown> | undefined;
async function signIn() {
  if (config.browser === 'safari') return beginNative();
  if (!browser.identity) throw new Failure('native_sign_in_required');
  const start = await serialized(state);
  const pending: Pending = {
    epoch: start.epoch,
    verifier: randomSecret(),
    state: randomSecret(),
    redirect: browser.identity.getRedirectURL('callback'),
    startedAt: Date.now(),
  };
  await browser.storage.local.set({ [pendingKey]: pending });
  const url = new URL('/extensoes/autorizar', config.siteURL);
  url.search = new URLSearchParams({
    client_id: config.clientID,
    redirect_uri: pending.redirect,
    code_challenge: await pkceChallenge(pending.verifier),
    code_challenge_method: 'S256',
    state: pending.state,
  }).toString();
  try {
    const callback = await browser.identity.launchWebAuthFlow({ url: url.href, interactive: true });
    if (!callback) throw new Failure('authorization_declined');
    const code = callbackCode(callback, pending.redirect, pending.state);
    const tokens = await tokenRequest({
      grant_type: 'authorization_code',
      code,
      redirect_uri: pending.redirect,
      code_verifier: pending.verifier,
    });
    await serialized(async () => {
      const current = await state();
      if (current.epoch !== pending.epoch) {
        await revoke(tokens).catch(() => undefined);
        throw new Failure('sign_in_cancelled');
      }
      await save({ epoch: crypto.randomUUID(), tokens });
    });
    return { signedIn: true };
  } finally {
    await browser.storage.local.remove(pendingKey);
  }
}
function native(message: Record<string, string>): Promise<Record<string, string>> {
  if (!browser.runtime.sendNativeMessage) throw new Failure('native_sign_in_required');
  return browser.runtime.sendNativeMessage('docmatch', message) as Promise<Record<string, string>>;
}
function nativeOpenURL(id: string) {
  const url = new URL(config.redirectURI);
  url.hostname = 'authorize';
  url.search = new URLSearchParams({ request: id }).toString();
  return url.href;
}
async function clearPending(pending?: Pending) {
  await browser.storage.local.remove(pendingKey);
  if (pending?.nativeID)
    await native({ action: 'cancel', id: pending.nativeID }).catch(() => undefined);
}
async function beginNative() {
  return serialized(async () => {
    const current = await state();
    const previous = (await browser.storage.local.get(pendingKey))[pendingKey] as
      Pending | undefined;
    await clearPending(previous);
    const pending: Pending = {
      epoch: current.epoch,
      verifier: randomSecret(),
      state: randomSecret(),
      redirect: config.redirectURI,
      startedAt: Date.now(),
    };
    const response = await native({
      action: 'begin',
      state: pending.state,
      challenge: await pkceChallenge(pending.verifier),
    });
    if (
      !/^[0-9a-f-]{36}$/i.test(response.id ?? '') ||
      response.openURL !== nativeOpenURL(response.id!)
    )
      throw new Failure('invalid_response');
    pending.nativeID = response.id!;
    await browser.storage.local.set({ [pendingKey]: pending });
    return { signedIn: false, openURL: nativeOpenURL(response.id!) };
  });
}
async function status() {
  return serialized(async () => {
    let current = await state();
    const pending = (await browser.storage.local.get(pendingKey))[pendingKey] as
      Pending | undefined;
    if (config.browser === 'safari' && pending?.nativeID) {
      if (pending.epoch !== current.epoch || Date.now() - pending.startedAt >= 600000) {
        await clearPending(pending);
      } else {
        const response = await native({ action: 'poll', id: pending.nativeID });
        if (
          response.error ||
          response.state !== pending.state ||
          !['pending', 'ready'].includes(response.status ?? '')
        ) {
          await clearPending(pending);
          throw new Failure('authorization_declined');
        }
        if (response.status === 'ready') {
          if (!/^[A-Za-z0-9_-]{43}$/.test(response.code ?? '')) {
            await clearPending(pending);
            throw new Failure('invalid_response');
          }
          try {
            const tokens = await tokenRequest({
              grant_type: 'authorization_code',
              code: response.code,
              redirect_uri: pending.redirect,
              code_verifier: pending.verifier,
            });
            current = { epoch: crypto.randomUUID(), tokens };
            await save(current);
            await clearPending(pending);
          } catch (error) {
            if (error instanceof Failure) await clearPending(pending);
            throw error;
          }
        } else
          return {
            signedIn: !!current.tokens,
            epoch: current.epoch,
            openURL: nativeOpenURL(pending.nativeID),
          };
      }
    }
    return { signedIn: !!current.tokens, epoch: current.epoch };
  });
}
async function credentials(
  expectedEpoch: string,
  force = false,
  oldAccess?: string,
): Promise<Tokens> {
  return serialized(async () => {
    const current = await state();
    if (current.epoch !== expectedEpoch) throw new Failure('session_changed', 401);
    const t = current.tokens;
    if (!t || Date.parse(t.refresh_expires_at) <= Date.now()) {
      await save({ epoch: crypto.randomUUID() });
      throw new Failure('sign_in_required', 401);
    }
    if (Date.parse(t.expires_at) > Date.now() + 30_000 && (!force || t.access_token !== oldAccess))
      return t;
    // Persist before network I/O: a restarted worker repeats the same rotation.
    current.refreshRequestID ??= crypto.randomUUID();
    await save(current);
    try {
      const tokens = await tokenRequest({
        grant_type: 'refresh_token',
        refresh_token: t.refresh_token,
        request_id: current.refreshRequestID,
      });
      await save({ epoch: current.epoch, tokens });
      return tokens;
    } catch (error) {
      if (error instanceof Failure && error.status === 401)
        await save({ epoch: crypto.randomUUID() });
      throw error;
    }
  });
}
function revoke(t: Tokens) {
  return fetch(new URL('/v1/extensions/revoke', config.apiURL), {
    method: 'POST',
    headers: { Authorization: `Bearer ${t.access_token}` },
    credentials: 'omit',
    cache: 'no-store',
    redirect: 'error',
    signal: AbortSignal.timeout(10000),
  });
}
async function signOut() {
  return serialized(async () => {
    const current = await state();
    await save({ epoch: crypto.randomUUID() });
    await clearPending(
      (await browser.storage.local.get(pendingKey))[pendingKey] as Pending | undefined,
    );
    if (current.tokens) await revoke(current.tokens).catch(() => undefined);
    return { signedIn: false };
  });
}
async function apiResponse(request: APIRequest, signal?: AbortSignal) {
  const token = await credentials(request.sessionEpoch);
  const send = (t: Tokens) => {
    const headers: Record<string, string> = {
      Accept: 'application/json',
      Authorization: `Bearer ${t.access_token}`,
    };
    if (request.orgId) headers['X-Docmatch-Org'] = request.orgId;
    if (request.idempotencyKey) headers['Idempotency-Key'] = request.idempotencyKey;
    if (request.body !== undefined) headers['Content-Type'] = 'application/json';
    return fetch(new URL(request.path, config.apiURL), {
      method: request.method ?? (request.body === undefined ? 'GET' : 'POST'),
      headers,
      credentials: 'omit',
      cache: 'no-store',
      redirect: 'error',
      signal: signal ?? AbortSignal.timeout(30000),
      ...(request.body === undefined ? {} : { body: JSON.stringify(request.body) }),
    });
  };
  let response = await send(token);
  if (response.status === 401)
    response = await send(await credentials(request.sessionEpoch, true, token.access_token));
  if (!response.ok) {
    const data: unknown = await response.json();
    const error = data as { code?: string; details?: unknown };
    throw new Failure(error.code ?? 'server_error', response.status, error.details);
  }
  return response;
}
async function api(request: APIRequest) {
  const response = await apiResponse(request);
  return response.status === 204 ? null : response.json();
}

// A foreground page pulls one chunk at a time. Files never become a giant JSON message,
// and disconnecting the page cancels the authenticated download in the background.
browser.runtime.onConnect.addListener((port) => {
  if (
    port.name !== 'docmatch.export' ||
    !trustedPage(browser.runtime.id, port.sender ?? {}, (path) => browser.runtime.getURL(path))
  ) {
    port.disconnect();
    return;
  }
  const controller = new AbortController();
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  let remainder = new Uint8Array(0);
  let epoch: string | undefined;
  let closed = false;
  let terminal = false;
  let work: Promise<unknown> = Promise.resolve();
  let timer: ReturnType<typeof setTimeout>;
  const close = () => {
    if (closed) return;
    closed = true;
    clearTimeout(timer);
    controller.abort();
    void reader?.cancel().catch(() => undefined);
    port.disconnect();
  };
  const resetTimeout = () => {
    clearTimeout(timer);
    timer = setTimeout(close, 30000);
  };
  port.onDisconnect.addListener(close);
  resetTimeout();
  port.onMessage.addListener((message) => {
    work = work
      .then(async () => {
        await ready;
        if (closed || terminal) return;
        resetTimeout();
        const m = message as { type?: string; request?: unknown } | null;
        if (m?.type === 'start' && !epoch && validExportRequest(m.request)) {
          epoch = m.request.sessionEpoch;
          const response = await apiResponse(m.request, controller.signal);
          if (!response.body) throw new Failure('invalid_response');
          reader = response.body.getReader();
          const format = new URLSearchParams(m.request.path.split('?')[1]).get('format');
          port.postMessage({
            type: 'ready',
            mime:
              format === 'pdf'
                ? 'application/pdf'
                : format === 'xlsx'
                  ? 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'
                  : 'text/csv',
          });
        } else if (m?.type === 'next' && reader && epoch) {
          if ((await state()).epoch !== epoch) throw new Failure('session_changed', 401);
          if (!remainder.length) {
            const part = await reader.read();
            if (part.done) {
              terminal = true;
              port.postMessage({ type: 'end' });
              // Let the receiver close after consuming the final message. Safari can
              // deliver onDisconnect before a message queued immediately before it.
              return;
            }
            remainder = new Uint8Array(part.value);
          }
          const chunk = remainder.slice(0, 32 * 1024);
          remainder = remainder.subarray(chunk.length);
          if (!closed) port.postMessage({ type: 'chunk', data: base64url(chunk) });
        } else throw new Failure('invalid_request');
      })
      .catch((error: unknown) => {
        if (!closed) {
          terminal = true;
          controller.abort();
          void reader?.cancel().catch(() => undefined);
          port.postMessage({
            type: 'error',
            code: error instanceof Failure ? error.code : 'connection_failed',
            status: error instanceof Failure ? error.status : 0,
          });
        }
      });
  });
});
browser.runtime.onMessage.addListener((message, sender, respond) => {
  if (!trustedPage(browser.runtime.id, sender, (path) => browser.runtime.getURL(path)))
    return false;
  void (async () => {
    await ready;
    if (!message || typeof message !== 'object') throw new Failure('invalid_request');
    const m = message as { type?: string; request?: unknown };
    switch (m.type) {
      case 'signIn':
        signingIn ??= signIn().finally(() => {
          signingIn = undefined;
        });
        return signingIn;
      case 'signOut':
        return signOut();
      case 'status':
        return status();
      case 'api':
        if (!validAPIRequest(m.request)) throw new Failure('invalid_request');
        return api(m.request);
      default:
        throw new Failure('invalid_request');
    }
  })().then(
    (value) => respond({ ok: true, value }),
    (error: unknown) => {
      // Never serialize Error objects or fetch request bodies: they can contain credentials.
      respond({
        ok: false,
        error:
          error instanceof Failure
            ? { code: error.code, status: error.status, details: error.details }
            : { code: 'connection_failed', status: 0 },
      });
    },
  );
  return true;
});
