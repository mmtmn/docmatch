// Client-safe environment. Only PUBLIC_ values declared in astro.config.ts reach the browser.
import {
  PUBLIC_DM_API_BASE_URL,
  PUBLIC_DM_FEATURE_RESULTS,
  PUBLIC_DM_MEDIA_BASE_URL,
} from 'astro:env/client';

function normalizeBase(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  if (!trimmed) return undefined;
  return trimmed.replace(/\/+$/, '');
}

export const apiBaseUrl = normalizeBase(PUBLIC_DM_API_BASE_URL);
export const mediaBaseUrl = normalizeBase(PUBLIC_DM_MEDIA_BASE_URL);
export const featureResults = PUBLIC_DM_FEATURE_RESULTS;

/** Absolute API URL for a path, or undefined when the API origin is not configured. */
export function apiUrl(path: string): string | undefined {
  if (!apiBaseUrl) return undefined;
  return `${apiBaseUrl}${path.startsWith('/') ? path : `/${path}`}`;
}
