import { defineConfig, devices } from '@playwright/test';
import { browserEnv } from './tools/browser-env.mjs';

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
    launchOptions: { env: browserEnv() },
    baseURL,
    ...(process.env['DM_E2E_CHANNEL'] ? { channel: process.env['DM_E2E_CHANNEL'] } : {}),
    locale: 'pt-BR',
    timezoneId: 'America/Sao_Paulo',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
