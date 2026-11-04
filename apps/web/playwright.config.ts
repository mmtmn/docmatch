import { defineConfig, devices } from '@playwright/test';

// The suite runs against the production build (`astro preview` over dist/) so the CSP meta and
// the real bundles are exercised. Build with PUBLIC_DM_FEATURE_RESULTS=true first (make e2e).
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
    command: `pnpm exec astro preview --host ${host} --port ${port}`,
    url: baseURL,
    reuseExistingServer: !isCI,
    timeout: 60_000,
  },
});
