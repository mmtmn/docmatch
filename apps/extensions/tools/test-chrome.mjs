// Real local acceptance. Uses an isolated profile and synthetic repository fixtures.
import { chromium } from '../../web/node_modules/@playwright/test/index.mjs';
import AxeBuilder from '../../web/node_modules/@axe-core/playwright/dist/index.mjs';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
const settings = JSON.parse(
  await fs.readFile(path.join(root, 'apps/extensions/local.config.json'), 'utf8'),
);
for (const value of [settings.apiURL, settings.siteURL])
  assert(['localhost', '127.0.0.1'].includes(new URL(value).hostname), 'local server only');
const texts = JSON.parse(await fs.readFile(path.join(root, 'packages/locales/pt-BR.json'), 'utf8'));
const extension = path.join(root, 'apps/extensions/dist/local/chrome');
const profile = await fs.mkdtemp(path.join(os.tmpdir(), 'docmatch-extension-e2e-'));
const artifacts = path.join(root, '.cache/extensions-e2e');
await fs.mkdir(artifacts, { recursive: true });
const context = await chromium.launchPersistentContext(profile, {
  channel: 'chromium',
  headless: true,
  acceptDownloads: true,
  viewport: { width: 1280, height: 900 },
  args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
});
context.setDefaultTimeout(20000);
const errors = [],
  network = [];
let starts = 0;
context.on('page', (page) => page.on('pageerror', (error) => errors.push(error.message)));
context.on('request', (request) => {
  const url = new URL(request.url());
  if (url.origin !== settings.apiURL) return;
  if (request.method() === 'POST' && /^\/v1\/projects\/[^/]+\/runs$/.test(url.pathname)) starts++;
  network.push(
    Promise.race([
      request.allHeaders(),
      new Promise((resolve) => {
        const timer = setTimeout(() => resolve(null), 5000);
        timer.unref();
      }),
    ])
      .then(
        (headers) =>
          headers && {
            extension: headers.authorization?.startsWith('Bearer dmxA1_') ?? false,
            exchange: url.pathname === '/v1/extensions/token',
            storage:
              (!url.pathname.startsWith('/v1/') || url.pathname.startsWith('/v1/_local/blobs/')) &&
              ['POST', 'PUT'].includes(request.method()),
            cookie: !!headers.cookie,
            authorization: !!headers.authorization,
          },
      )
      .catch(() => null),
  );
});
try {
  const worker = context.serviceWorkers()[0] ?? (await context.waitForEvent('serviceworker'));
  assert.equal(new URL(worker.url()).host, settings.chrome.id);
  let page = await context.newPage();
  await page.goto(`chrome-extension://${settings.chrome.id}/workspace.html`);
  const authOpened = context.waitForEvent('page');
  await page.getByRole('button', { name: texts.nav.login, exact: true }).click();
  const auth = await authOpened;
  await auth.waitForURL((url) => url.origin === settings.siteURL);
  await auth.getByRole('button', { name: texts.nav.login, exact: true }).click();
  await auth.getByRole('button', { name: /ambiente local/ }).click();
  await auth.getByRole('button', { name: texts.extensions.allow, exact: true }).click();
  await page.getByRole('combobox', { name: texts.extensions.account, exact: true }).waitFor();
  await page
    .getByLabel(texts.projects.name, { exact: true })
    .fill('Fechamento pela extensão Chrome');
  await page.getByRole('button', { name: texts.projects.new, exact: true }).click();
  await page.waitForURL(/project=/);
  const projectURL = page.url();
  await page
    .getByLabel(texts.projects.add_documents, { exact: true })
    .setInputFiles(
      ['01_contas_a_receber_protheus_jul2026.csv', '03_extrato_bancario_jul2026.csv'].map((file) =>
        path.join(root, 'testdata/golden/vantara-twin', file),
      ),
    );
  await page.getByText(texts.projects.upload_saved, { exact: true }).waitFor({ timeout: 60000 });
  const action = page.getByRole('button', { name: texts.projects.reconcile, exact: true });
  await action.click();
  await page.getByRole('button', { name: texts.app.confirm_start, exact: true }).click();
  await page.waitForURL(/run=/);
  const resultsURL = page.url();
  // The server continues after the foreground page closes. The restarted background recovers credentials.
  const cdp = await context.newCDPSession(page);
  await cdp.send('ServiceWorker.enable');
  await cdp.send('ServiceWorker.stopAllWorkers');
  await page.close();
  page = await context.newPage();
  await page.goto(resultsURL);
  await page
    .getByRole('heading', { name: texts.results.document_ready, exact: true })
    .waitFor({ timeout: 90000 });
  const downloading = page.waitForEvent('download');
  await page.getByRole('button', { name: texts.results.download_corrected, exact: true }).click();
  const download = await downloading;
  const file = path.join(artifacts, download.suggestedFilename());
  await download.saveAs(file);
  assert((await fs.stat(file)).size > 1000);
  assert.equal(await download.failure(), null);
  await page.getByText(texts.results.all_heading, { exact: true }).click();
  await page.getByTestId('finding-row').first().waitFor();
  await page.getByTestId('finding-row').first().getByRole('button').click();
  await page.getByRole('heading', { name: texts.results.evidence_heading, exact: true }).waitFor();
  await page
    .getByRole('dialog')
    .getByRole('button', { name: texts.results.drawer_close, exact: true })
    .click();
  await page.goto(projectURL);
  await page.getByLabel(texts.projects.name, { exact: true }).waitFor();
  await page.getByRole('button', { name: texts.projects.reconcile, exact: true }).click();
  await page.waitForURL(/run=/);
  assert.equal(page.url(), resultsURL);
  assert.equal(starts, 1, 'unchanged reconciliation must not create another run');
  await page.goto(projectURL);
  await page.getByLabel(texts.projects.name, { exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  const accessibility = await new AxeBuilder({ page }).analyze();
  assert.deepEqual(
    accessibility.violations.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) })),
    [],
  );
  await page.screenshot({ path: path.join(artifacts, 'chrome-project.png'), fullPage: true });
  const observed = (await Promise.all(network)).filter(Boolean);
  assert(observed.some((r) => r.extension));
  assert(observed.some((r) => r.exchange));
  assert(observed.filter((r) => r.storage).length >= 2);
  assert(
    !observed.some((r) => (r.extension || r.exchange || r.storage) && r.cookie),
    'credentials must not reuse website cookies',
  );
  assert(
    !observed.some((r) => r.storage && r.authorization),
    'signed uploads must not carry API authorization',
  );
  assert.deepEqual(errors, []);
  console.log(
    JSON.stringify({
      chrome: 'passed',
      uploads: 2,
      reconciliation_posts: starts,
      reused: true,
      background_restart: true,
      export_bytes: (await fs.stat(file)).size,
      evidence: true,
      cookie_isolation: true,
      axe: 0,
    }),
  );
} catch (error) {
  console.error('Acceptance failure:', error.message);
  for (const page of context
    .pages()
    .filter((page) => page.url().startsWith(`chrome-extension://${settings.chrome.id}/`))) {
    console.error('Extension alerts:', await page.locator('[role="alert"]').allTextContents());
    await page
      .screenshot({ path: path.join(artifacts, 'chrome-failure.png'), fullPage: true })
      .catch(() => {});
  }
  console.error('Browser errors:', errors);
  throw error;
} finally {
  await context.close();
  await fs.rm(profile, { recursive: true, force: true });
}
