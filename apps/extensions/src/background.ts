import { browser } from './browser';
import { config } from './config';
import {
  callbackCode,
  pkceChallenge,
  randomSecret,
  trustedPage,
  validAPIRequest,
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
    await browser.storage.local.remove(pendingKey);
    if (current.tokens) await revoke(current.tokens).catch(() => undefined);
    return { signedIn: false };
  });
}
async function api(request: APIRequest) {
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
      signal: AbortSignal.timeout(30000),
      ...(request.body === undefined ? {} : { body: JSON.stringify(request.body) }),
    });
  };
  let response = await send(token);
  if (response.status === 401)
    response = await send(await credentials(request.sessionEpoch, true, token.access_token));
  const data: unknown = response.status === 204 ? null : await response.json();
  if (!response.ok) {
    const error = data as { code?: string; details?: unknown };
    throw new Failure(error.code ?? 'server_error', response.status, error.details);
  }
  return data;
}
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
        return serialized(async () => {
          const s = await state();
          return { signedIn: !!s.tokens, epoch: s.epoch };
        });
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
