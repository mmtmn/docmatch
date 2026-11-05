import { defineConfig, devices } from '@playwright/test';

// The live local flow (make e2e-local): the whole stack runs on this machine through
// tools/dev/local.sh, so there is no webServer here; the run itself takes a while.
const baseURL = process.env['DM_LOCAL_WEB_URL'] ?? 'http://localhost:4321';

export default defineConfig({
  testDir: './e2e-local',
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  timeout: 180_000,
  expect: { timeout: 60_000 },
  use: {
    baseURL,
    locale: 'pt-BR',
    timezoneId: 'America/Sao_Paulo',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
