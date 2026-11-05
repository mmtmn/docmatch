import { defineConfig, devices } from '@playwright/test';

// The site recording for docs/media (tools/media/media.sh site): one test, recorded on video,
// against a running local stack (the installation the recording script brings up, or make local).
const baseURL = process.env['DM_LOCAL_WEB_URL'] ?? 'http://localhost:4321';
const size = { width: 1200, height: 760 };

export default defineConfig({
  testDir: './e2e-media',
  outputDir: process.env['DM_MEDIA_VIDEO_DIR'] ?? 'test-results/media',
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  timeout: 300_000,
  expect: { timeout: 120_000 },
  use: {
    baseURL,
    locale: 'pt-BR',
    timezoneId: 'America/Sao_Paulo',
    colorScheme: 'light',
    video: { mode: 'on', size },
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'], viewport: size, deviceScaleFactor: 1 },
    },
  ],
});
