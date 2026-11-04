// Test double for `astro:env/server` (see vitest.config.ts).
export const PUBLIC_DM_SECURITY_CONTACT: string | undefined =
  process.env['PUBLIC_DM_SECURITY_CONTACT'];
export const PUBLIC_DM_SECURITY_POLICY_URL: string | undefined =
  process.env['PUBLIC_DM_SECURITY_POLICY_URL'];
export const PUBLIC_DM_SOURCE_URL: string | undefined = process.env['PUBLIC_DM_SOURCE_URL'];
export const PUBLIC_DM_STATUS_URL: string | undefined = process.env['PUBLIC_DM_STATUS_URL'];
