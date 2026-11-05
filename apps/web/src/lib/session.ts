// Session helpers over the auth routes. Nothing is stored in the browser: the API sets the
// HttpOnly cookies and /v1/me says who is signed in.
import { api, ApiError } from './api';

export interface Me {
  user: { id: string; email: string; display_name: string };
  org: { id: string; name: string };
  credits?: { balance: number; reserved: number; available: number };
}

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
