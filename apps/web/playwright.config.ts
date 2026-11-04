import { defineConfig, devices } from '@playwright/test';

// The suite runs against the production build: tools/serve-dist.mjs serves dist/ in the foreground
// with the same clean-URL rules as CloudFront (docs/headers.md), so the CSP meta and the real
// bundles are exercised. Build with PUBLIC_DM_FEATURE_RESULTS=true first (make e2e, which also
// picks a free port per run through DM_WEB_E2E_PORT).
const host = process.env['DM_WEB_E2E_HOST'] ?? '127.0.0.1';
const port = Number(process.env['DM_WEB_E2E_PORT'] ?? 4173);
const baseURL = `http://${host}:${port}`;
const browsers = (process.env['DM_E2E_BROWSERS'] ?? 'chromium').split(',').map((b) => b.trim());
const isCI = Boolean(process.env['CI']);

const projects = [
  browsers.includes('chromium') && { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  browsers.includes('firefox') && { name: 'firefox', use: { ...devices['Desktop Firefox'] } },
  browsers.includes('webkit') && { name: 'webkit', use: { ...devices['Desktop Safari'] } },
].filter((project): project is Exclude<typeof project, false> => Boolean(project));

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: isCI,
  retries: isCI ? 1 : 0,
  ...(isCI ? { workers: 4 } : {}),
  reporter: isCI ? [['list'], ['html', { open: 'never' }]] : [['list']],
  timeout: 30_000,
  expect: { timeout: 5_000 },
  use: {
    baseURL,
    locale: 'pt-BR',
    timezoneId: 'America/Sao_Paulo',
    trace: 'retain-on-failure',
  },
  projects,
  webServer: {
    // Not `astro preview`: Astro 7 daemonizes it when run by an agent or without a TTY, so the
    // process "exited early" and whatever server happened to listen on the port got tested.
    command: `node tools/serve-dist.mjs --host ${host} --port ${port}`,
    url: baseURL,
    // Never attach to a server this run did not start: a busy port fails fast.
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
