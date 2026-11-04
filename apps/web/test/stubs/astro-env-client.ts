// Test double for `astro:env/client` (see vitest.config.ts). Values mirror the schema declared in
// astro.config.ts and can be set through the environment before running vitest.
export const PUBLIC_DM_API_BASE_URL: string | undefined = process.env['PUBLIC_DM_API_BASE_URL'];
export const PUBLIC_DM_MEDIA_BASE_URL: string | undefined = process.env['PUBLIC_DM_MEDIA_BASE_URL'];
export const PUBLIC_DM_FEATURE_RESULTS: boolean =
  process.env['PUBLIC_DM_FEATURE_RESULTS'] === 'true';
