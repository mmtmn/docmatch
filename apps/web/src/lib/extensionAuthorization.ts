import { api } from './api';

export interface ExtensionAuthorization {
  client_id: string;
  redirect_uri: string;
  code_challenge: string;
  code_challenge_method: 'S256';
  state: string;
}

/** Public parameters only. PKCE verifiers and access credentials never enter this page. */
export function readExtensionAuthorization(search: string): ExtensionAuthorization | null {
  const params = new URLSearchParams(search);
  const keys = ['client_id', 'redirect_uri', 'code_challenge', 'code_challenge_method', 'state'];
  if (keys.some((key) => params.getAll(key).length !== 1)) return null;
  const client_id = params.get('client_id') ?? '';
  const redirect_uri = params.get('redirect_uri') ?? '';
  const code_challenge = params.get('code_challenge') ?? '';
  const state = params.get('state') ?? '';
  if (
    !/^[a-z][a-z0-9_-]{2,63}$/.test(client_id) ||
    !/^[A-Za-z0-9_-]{43}$/.test(code_challenge) ||
    !/^[A-Za-z0-9_-]{43}$/.test(state) ||
    params.get('code_challenge_method') !== 'S256' ||
    redirect_uri.length > 2048
  )
    return null;
  try {
    const target = new URL(redirect_uri);
    if (
      !['https:', 'docmatch-extension:', 'docmatch-extension-dev:'].includes(target.protocol) ||
      !target.host ||
      target.username ||
      target.password ||
      target.search ||
      target.hash
    )
      return null;
  } catch {
    return null;
  }
  return { client_id, redirect_uri, code_challenge, code_challenge_method: 'S256', state };
}

export function extensionClient(request: ExtensionAuthorization, signal?: AbortSignal) {
  return api<{ id: string; name: string }>(
    `/v1/extensions/clients/${encodeURIComponent(request.client_id)}?${new URLSearchParams({ redirect_uri: request.redirect_uri })}`,
    { orgId: null, ...(signal ? { signal } : {}) },
  );
}

export async function authorizeExtension(request: ExtensionAuthorization): Promise<string> {
  const result = await api<{ redirect_uri: string }>('/v1/extensions/authorize', {
    orgId: null,
    body: request,
  });
  const target = new URL(result.redirect_uri);
  const expected = new URL(request.redirect_uri);
  if (
    target.protocol !== expected.protocol ||
    target.host !== expected.host ||
    target.pathname !== expected.pathname ||
    target.username ||
    target.password ||
    target.hash ||
    target.searchParams.get('state') !== request.state ||
    !/^[A-Za-z0-9_-]{43}$/.test(target.searchParams.get('code') ?? '')
  )
    throw new Error('invalid_authorization_response');
  return target.href;
}
