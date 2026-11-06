export interface APIRequest {
  sessionEpoch: string;
  path: string;
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  orgId?: string;
  body?: unknown;
  idempotencyKey?: string;
}
const orgID = /^org_[0-9A-HJKMNP-TV-Z]{26}$/;
const methods = new Set(['GET', 'POST', 'PATCH', 'PUT', 'DELETE']);
export function trustedPage(
  id: string,
  sender: { id?: string; url?: string },
  getURL: (p: string) => string,
): boolean {
  if (sender.id !== id || !sender.url) return false;
  try {
    const target = new URL(sender.url);
    target.search = '';
    target.hash = '';
    return ['popup.html', 'workspace.html'].some((path) => target.href === getURL(path));
  } catch {
    return false;
  }
}
export function validAPIRequest(value: unknown): value is APIRequest {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const r = value as Record<string, unknown>;
  if (typeof r.sessionEpoch !== 'string' || !/^[0-9a-f-]{36}$/.test(r.sessionEpoch)) return false;
  if (
    typeof r.path !== 'string' ||
    r.path.length > 4096 ||
    !r.path.startsWith('/') ||
    r.path.includes('\\')
  )
    return false;
  const [path] = r.path.split('?');
  const method = r.method ?? (r.body === undefined ? 'GET' : 'POST');
  if (typeof method !== 'string' || !methods.has(method)) return false;
  const permitted =
    /^\/v1\/projects(?:\/[A-Za-z0-9_-]+){0,6}$/.test(path!) ||
    (method === 'GET' && ['/v1/me', '/v1/me/organizations'].includes(path!)) ||
    (method === 'GET' && /^\/v1\/uploads\/upl_[0-9A-HJKMNP-TV-Z]{26}$/.test(path!));
  if (
    !permitted ||
    (path!.startsWith('/v1/projects') && r.orgId === undefined) ||
    (r.orgId !== undefined && (typeof r.orgId !== 'string' || !orgID.test(r.orgId)))
  )
    return false;
  if (
    r.idempotencyKey !== undefined &&
    (typeof r.idempotencyKey !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(r.idempotencyKey))
  )
    return false;
  try {
    if (
      r.body !== undefined &&
      new TextEncoder().encode(JSON.stringify(r.body)).byteLength > 256 * 1024
    )
      return false;
  } catch {
    return false;
  }
  return true;
}
export function validExportRequest(value: unknown): value is APIRequest {
  if (
    !validAPIRequest(value) ||
    value.body !== undefined ||
    (value.method && value.method !== 'GET')
  )
    return false;
  return (
    /^\/v1\/projects\/prj_[0-9A-HJKMNP-TV-Z]{26}\/runs\/run_[0-9A-HJKMNP-TV-Z]{26}\/export\?format=(csv|xlsx|pdf)$/.test(
      value.path,
    ) && !!value.orgId
  );
}
export function callbackCode(callback: string, expected: string, state: string): string {
  const url = new URL(callback);
  const redirect = new URL(expected);
  if (
    url.protocol !== redirect.protocol ||
    url.host !== redirect.host ||
    url.pathname !== redirect.pathname ||
    url.username ||
    url.password ||
    url.hash ||
    url.searchParams.getAll('state').length !== 1 ||
    url.searchParams.get('state') !== state
  )
    throw new Error('invalid_callback');
  if (url.searchParams.get('error')) throw new Error('authorization_declined');
  const code = url.searchParams.get('code');
  if (url.searchParams.getAll('code').length !== 1 || !code || !/^[A-Za-z0-9_-]{43}$/.test(code))
    throw new Error('invalid_callback');
  return code;
}
export function randomSecret(): string {
  return base64url(crypto.getRandomValues(new Uint8Array(32)));
}
export function base64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes))
    .replaceAll('+', '-')
    .replaceAll('/', '_')
    .replaceAll('=', '');
}
export async function pkceChallenge(verifier: string): Promise<string> {
  return base64url(
    new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))),
  );
}
