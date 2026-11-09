import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import type { DashboardData } from '../src/lib/dashboard';
import { m } from './messages';

const orgA = 'org_00000000000000000000000001';
const orgB = 'org_00000000000000000000000002';
const user = 'usr_00000000000000000000000001';
const capabilities = {
  manage_account: true,
  create_project: true,
  reconcile: true,
  read_results: true,
  upload: true,
};
const metrics = {
  runs: 187,
  completed: 174,
  failed: 6,
  cancelled: 2,
  queued: 1,
  running: 3,
  needs_approval: 1,
  credits_spent: 24860,
  lines_total: 34000,
  lines_matched: 32040,
  findings: 86,
  high_findings: 9,
  median_seconds: 42,
};
function fixture(days: 7 | 30 | 90 = 30): DashboardData {
  const to = new Date('2026-11-09T15:00:00Z');
  const from = new Date(to.getTime() - days * 86400000);
  return {
    generated_at: to.toISOString(),
    from: from.toISOString(),
    previous_from: new Date(from.getTime() - days * 86400000).toISOString(),
    days,
    time_zone: 'America/Sao_Paulo',
    current: { ...metrics },
    previous: { ...metrics, runs: 150, completed: 140, credits_spent: 22100 },
    portfolio: { active: 12, archived: 3, documents: 246, unavailable_documents: 1 },
    series: Array.from({ length: days + 1 }, (_, index) => ({
      date: new Date(from.getTime() + index * 86400000).toISOString().slice(0, 10),
      runs: [3, 5, 4, 8, 6, 9, 3, 6][index % 8] ?? 0,
      completed: 3,
      failed: index % 7 === 0 ? 1 : 0,
      credits_spent: 300 + index * 35,
    })),
    recent: [
      {
        id: 'run_approval',
        project_id: 'prj_finance',
        project_name: 'Fechamento de novembro',
        status: 'needs_approval',
        created_at: '2026-11-09T14:50:00Z',
        expired: false,
        findings: 0,
      },
      {
        id: 'run_complete',
        project_id: 'prj_fees',
        project_name: 'Honorários e contratos',
        status: 'completed',
        created_at: '2026-11-09T14:30:00Z',
        expired: false,
        findings: 2,
      },
      {
        id: 'run_failed',
        project_id: 'prj_invoices',
        project_name: 'Notas fiscais · fornecedores',
        status: 'failed',
        created_at: '2026-11-09T13:30:00Z',
        expired: false,
        findings: 0,
      },
      {
        id: 'run_expired',
        project_id: '',
        project_name: '',
        status: 'completed',
        created_at: '2026-10-15T12:00:00Z',
        expired: true,
        findings: 3,
      },
    ],
    attention: [
      {
        id: 'run_approval',
        project_id: 'prj_finance',
        project_name: 'Fechamento de novembro',
        status: 'needs_approval',
        created_at: '2026-11-09T14:50:00Z',
        expired: false,
        findings: 0,
      },
    ],
    projects: [
      {
        id: 'prj_finance',
        name: 'Fechamento de novembro',
        documents: 42,
        updated_at: '2026-11-09T14:50:00Z',
        last_status: 'needs_approval',
        findings: 0,
      },
      {
        id: 'prj_fees',
        name: 'Honorários e contratos',
        documents: 18,
        updated_at: '2026-11-09T14:30:00Z',
        last_status: 'completed',
        findings: 2,
      },
      {
        id: 'prj_invoices',
        name: 'Notas fiscais · fornecedores',
        documents: 63,
        updated_at: '2026-11-09T13:30:00Z',
        last_status: 'failed',
        findings: 0,
      },
      {
        id: 'prj_bank',
        name: 'Conciliação bancária',
        documents: 28,
        updated_at: '2026-11-08T13:30:00Z',
        last_status: 'completed',
        findings: 0,
      },
    ],
  };
}
async function mock(
  page: Page,
  options: { empty?: boolean; uploader?: boolean; signedOut?: boolean; failOnce?: boolean } = {},
) {
  const requests: string[] = [];
  let failed = false;
  await page.route('**/v1/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const headers = {
      'Access-Control-Allow-Origin': request.headers()['origin'] ?? '*',
      'Access-Control-Allow-Credentials': 'true',
      'Access-Control-Allow-Headers': 'content-type,x-docmatch-org',
      'Access-Control-Allow-Methods': 'GET,POST',
    };
    if (request.method() === 'OPTIONS') {
      await route.fulfill({ status: 204, headers });
      return;
    }
    if (options.signedOut) {
      await route.fulfill({
        status: 401,
        headers,
        json: { code: 'unauthenticated', message: 'Entre novamente.' },
      });
      return;
    }
    const caps = options.uploader
      ? { ...capabilities, read_results: false, manage_account: false }
      : capabilities;
    const orgs = [
      {
        id: orgA,
        name: 'Vértice · Operações',
        kind: 'team',
        role: options.uploader ? 'uploader' : 'owner',
        plan: 'starter',
        capabilities: caps,
      },
      {
        id: orgB,
        name: 'Minha outra empresa',
        kind: 'team',
        role: 'owner',
        plan: 'starter',
        capabilities,
      },
    ];
    let body: unknown = { items: [] };
    if (url.pathname === '/v1/me/organizations') body = { user_id: user, items: orgs };
    if (url.pathname === '/v1/me')
      body = {
        user: { id: user, email: 'reader@example.test', display_name: 'Leitora' },
        org: { id: orgA, name: 'Vértice' },
      };
    if (url.pathname === '/v1/projects/dashboard') {
      requests.push(request.headers()['x-docmatch-org'] ?? '');
      if (options.failOnce && !failed) {
        failed = true;
        await route.fulfill({
          status: 503,
          headers,
          json: { code: 'service_unavailable', message: 'Tente novamente.' },
        });
        return;
      }
      const days = Number(url.searchParams.get('days')) as 7 | 30 | 90;
      const data = fixture(days);
      if (days === 7) data.current = { ...data.current, completed: 41 };
      if (options.empty || request.headers()['x-docmatch-org'] === orgB) {
        data.current = {
          ...metrics,
          runs: 0,
          completed: 0,
          failed: 0,
          cancelled: 0,
          queued: 0,
          running: 0,
          needs_approval: 0,
          credits_spent: 0,
          lines_total: 0,
          lines_matched: 0,
          findings: 0,
          high_findings: 0,
          median_seconds: null,
        };
        data.previous = { ...data.current };
        data.projects = [];
        data.recent = [];
        data.attention = [];
        data.portfolio = { active: 0, archived: 0, documents: 0, unavailable_documents: 0 };
        data.series = data.series.map((day) => ({
          ...day,
          runs: 0,
          completed: 0,
          failed: 0,
          credits_spent: 0,
        }));
      }
      body = data;
    }
    await route.fulfill({ headers, json: body });
  });
  return requests;
}

for (const width of [1440, 390]) {
  test(`dashboard is useful, accessible and fluid at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 });
    await mock(page);
    await page.goto(`/painel?account=${orgA}`);
    await expect(page.getByRole('heading', { name: m.dashboard.title, exact: true })).toBeVisible();
    await expect(
      page.getByRole('region', { name: m.dashboard.summary }).getByText('174', { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole('region', { name: m.dashboard.summary }).getByText('94,2%', { exact: true }),
    ).toBeVisible();
    await expect(page.getByRole('link').filter({ hasText: m.dashboard.expired })).toHaveCount(0);
    const action = page
      .getByRole('region', { name: m.dashboard.attention })
      .getByRole('link')
      .first();
    await expect(action).toHaveAttribute(
      'href',
      new RegExp(`account=${orgA}&run=run_approval&project=prj_finance`),
    );
    await page.getByRole('button', { name: m.dashboard.tokens, exact: true }).click();
    await expect(
      page.getByRole('button', { name: m.dashboard.tokens, exact: true }),
    ).toHaveAttribute('aria-pressed', 'true');
    await page.getByText(m.dashboard.table, { exact: true }).click();
    await expect(page.getByRole('table')).toBeVisible();
    await page.getByText(m.dashboard.table, { exact: true }).click();
    const scan = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
      .analyze();
    expect(scan.violations).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await page.screenshot({
      path: `../../scratch/dashboard-${width}-${testInfo.project.name}.png`,
      fullPage: true,
    });
    await page.getByRole('combobox', { name: m.dashboard.period }).selectOption('7');
    await expect(
      page.getByRole('region', { name: m.dashboard.summary }).getByText('41', { exact: true }),
    ).toBeVisible();
    await page.getByText(m.dashboard.definitions, { exact: true }).click();
    await expect(page.getByText(m.dashboard.definitions_body)).toBeVisible();
    const downloaded = page.waitForEvent('download');
    await page.getByRole('button', { name: m.dashboard.export }).click();
    expect((await downloaded).suggestedFilename()).toBe('docmatch-painel-2026-11-09.csv');
    await page.evaluate(() => {
      document.documentElement.style.fontSize = '200%';
    });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
  });
}

test('account switch keeps the dashboard and replaces the previous company data', async ({
  page,
}) => {
  const requests = await mock(page);
  await page.goto(`/painel?account=${orgA}`);
  await expect(
    page.getByRole('region', { name: m.dashboard.summary }).getByText('174', { exact: true }),
  ).toBeVisible();
  await page.getByRole('combobox', { name: m.projects.choose_account }).selectOption(orgB);
  await expect(page).toHaveURL(new RegExp(`/painel\\?account=${orgB}`));
  await expect(page.getByRole('heading', { name: m.dashboard.empty_title })).toBeVisible();
  await expect(page.getByText('Fechamento de novembro')).toHaveCount(0);
  expect(requests).toContain(orgA);
  expect(requests).toContain(orgB);
});

test('empty dashboard has a next step and does not invent rates or durations', async ({ page }) => {
  await mock(page, { empty: true });
  await page.goto('/painel');
  await expect(page.getByRole('heading', { name: m.dashboard.empty_title })).toBeVisible();
  await expect(page.getByText(m.dashboard.no_records)).toBeVisible();
  await expect(page.getByText(m.dashboard.no_duration)).toBeVisible();
  await expect(page.getByRole('link', { name: m.dashboard.projects_action })).toHaveAttribute(
    'href',
    '/app',
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test('failed dashboard requests are retryable without fabricated zeros', async ({ page }) => {
  await mock(page, { failOnce: true });
  await page.goto('/painel');
  await expect(page.getByRole('alert')).toHaveText(m.dashboard.error);
  await expect(page.getByRole('region', { name: m.dashboard.summary })).toHaveCount(0);
  await page.getByRole('button', { name: m.dashboard.retry }).click();
  await expect(page.getByRole('region', { name: m.dashboard.summary })).toBeVisible();
});

test('upload-only seats never request result metrics', async ({ page }) => {
  const requests = await mock(page, { uploader: true });
  await page.goto('/painel');
  await expect(page.getByText(m.dashboard.restricted)).toBeVisible();
  expect(requests).toEqual([]);
});

test('signed-out visitors get a sign-in route back to the dashboard', async ({ page }) => {
  await mock(page, { signedOut: true });
  await page.goto('/painel');
  await expect(page.getByText(m.dashboard.sign_in)).toBeVisible();
  await expect(page.getByRole('main').getByRole('link', { name: m.nav.login })).toHaveAttribute(
    'href',
    '/login?next=%2Fpainel',
  );
});

test('dashboard remains readable in dark mode', async ({ page }, testInfo) => {
  await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' });
  await mock(page);
  await page.goto('/painel');
  await expect(page.getByRole('region', { name: m.dashboard.summary })).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({
    path: `../../scratch/dashboard-dark-${testInfo.project.name}.png`,
    fullPage: true,
  });
});

test('existing sessions return from sign-in to the dashboard through an allowlisted path', async ({
  page,
}) => {
  await mock(page);
  await page.goto('/login?next=%2Fpainel');
  await expect(page).toHaveURL(/\/painel$/);
  await expect(page.getByRole('region', { name: m.dashboard.summary })).toBeVisible();
  await page.goto('/login?next=https%3A%2F%2Fexample.invalid');
  await expect(page).toHaveURL(/\/conta$/);
});
