// Session helpers over the auth routes. Nothing is stored in the browser: the API sets the
// HttpOnly cookies and /v1/me says who is signed in.
import { api, ApiError } from './api';

export interface Me {
  user: { id: string; email: string; display_name: string };
  org: { id: string; name: string };
  credits?: { balance: number; reserved: number; available: number };
}

/** Where the web app goes after signing in. */
export const APP_PATH = '/app';

/** The signed-in user, or null. */
export async function currentUser(signal?: AbortSignal): Promise<Me | null> {
  try {
    return await api<Me>('/v1/me', signal ? { signal } : {});
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return null;
    throw err;
  }
}

export interface EmailChallenge {
  challenge_id: string;
  expires_at: string;
}

/** Sends the 6-digit code and the magic link to `email`. */
export function startEmailLogin(email: string): Promise<EmailChallenge> {
  return api<EmailChallenge>('/v1/auth/email/start', { body: { email } });
}

/** Signs in with the typed code. */
export function verifyEmailLogin(challengeId: string, code: string): Promise<Me> {
  return api<Me>('/v1/auth/email/verify', { body: { challenge_id: challengeId, code } });
}

/** Starts the Google flow and leaves the page; `redirectTo` is a site-relative path. */
export async function startGoogleLogin(redirectTo: string): Promise<void> {
  const res = await api<{ authorization_url: string }>('/v1/auth/google/start', {
    body: { redirect_to: redirectTo },
  });
  window.location.assign(res.authorization_url);
}

export function logout(): Promise<void> {
  return api<void>('/v1/auth/logout', { method: 'POST' });
}

/** Which sign-ins this deployment offers (GET /v1/auth/methods). */
export interface AuthMethods {
  email: boolean;
  google: boolean;
  apple: boolean;
  password: boolean;
  /** Local sign-in without any provider: only on a development API. */
  dev: boolean;
}

const NO_METHODS: AuthMethods = {
  email: false,
  google: false,
  apple: false,
  password: false,
  dev: false,
};

/** The available sign-ins; none when the API cannot be reached. */
export async function authMethods(signal?: AbortSignal): Promise<AuthMethods> {
  try {
    return await api<AuthMethods>('/v1/auth/methods', signal ? { signal } : {});
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    return NO_METHODS;
  }
}

/** Local sign-in (development API only): creates the account on first use. */
export function devLogin(email?: string): Promise<Me> {
  return api<Me>('/v1/auth/dev/login', { body: email ? { email } : {} });
}

/** Signs in with the development login when the API offers it; null otherwise. */
export async function signInLocally(): Promise<Me | null> {
  const methods = await authMethods();
  if (!methods.dev) return null;
  await devLogin();
  return currentUser();
}

/** Starts the Apple flow and leaves the page; `redirectTo` is a site-relative path. */
export async function startAppleLogin(redirectTo: string): Promise<void> {
  const res = await api<{ authorization_url: string }>('/v1/auth/apple/start', {
    body: { redirect_to: redirectTo },
  });
  window.location.assign(res.authorization_url);
}

/** Confirms the magic link of the e-mail (challenge id and token from its URL). */
export function verifyMagicLink(challengeId: string, token: string): Promise<Me> {
  return verifyEmailLogin(challengeId, token);
}

export interface Device {
  id: string;
  platform: 'web' | 'android' | 'ios';
  name: string;
  created_at: string;
  last_seen_at: string;
  ip_prefix?: string;
  current: boolean;
}

/** Every signed-in browser and app of the account, the current one first. */
export async function listDevices(signal?: AbortSignal): Promise<Device[]> {
  const res = await api<{ items: Device[] }>('/v1/me/sessions', signal ? { signal } : {});
  return res.items;
}

/** Signs one device out (this one too: the API then clears the cookies). */
export function revokeDevice(id: string): Promise<void> {
  return api<void>(`/v1/me/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
}
