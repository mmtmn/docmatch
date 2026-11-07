import { AxeBuilder } from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { m } from './messages';

for (const width of [1280, 390]) {
  test(`paused projects retain saved work and the confirmed quick-run flow: ${width}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    const capabilities = {
      manage_account: true,
      create_project: false,
      reconcile: true,
      read_results: true,
      upload: true,
    };
    const org = {
      id: 'org_rollout',
      name: 'Conta de teste',
      kind: 'personal',
      role: 'owner',
      plan: 'starter',
      capabilities,
      features: { projects: false, invitations: false, extensions: false },
    };
    const project = {
      id: 'prj_saved',
      name: 'Fechamento salvo',
      visibility: 'private',
      version: 1,
      revision: 1,
      document_count: 0,
      created_at: '2026-11-06T12:00:00Z',
      updated_at: '2026-11-06T12:00:00Z',
      archived_at: null,
      capabilities: { ...capabilities, reconcile: false, upload: false },
      settings: { use_rules: false, web_search: false },
      documents: [],
    };
    const writes: { path: string; account: string | undefined }[] = [];
    await page.route('**/v1/**', async (route) => {
      const request = route.request(),
        path = new URL(request.url()).pathname;
      const headers = {
        'Access-Control-Allow-Origin': request.headers()['origin'] ?? '*',
        'Access-Control-Allow-Credentials': 'true',
        'Access-Control-Allow-Headers': 'content-type,x-docmatch-org,idempotency-key',
        'Access-Control-Allow-Methods': 'GET,POST,PATCH,DELETE,PUT',
      };
      if (request.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
      if (path === '/v1/workspace')
        return route.fulfill({
          status: 404,
          headers,
          json: { code: 'not_found', message: 'Sem personalização' },
        });
      if (request.method() === 'POST')
        writes.push({ path, account: request.headers()['x-docmatch-org'] });
      const body =
        path === '/v1/me/organizations'
          ? { user_id: 'usr_rollout', items: [org] }
          : path === '/v1/me'
            ? {
                user: { id: 'usr_rollout', email: 'rollout@example.test', display_name: 'Teste' },
                org,
                credits: { token_exempt: true },
              }
            : path === '/v1/projects'
              ? { items: [project] }
              : path === '/v1/projects/prj_saved'
                ? project
                : path === '/v1/uploads'
                  ? {
                      id: 'upl_quick',
                      method: 'PUT',
                      url: 'https://docmatch-api.example.invalid/storage/quick',
                      headers: {},
                    }
                  : path === '/v1/uploads/upl_quick/complete'
                    ? { id: 'upl_quick', file_name: 'extrato.csv', status: 'ready' }
                    : path === '/v1/runs/estimate'
                      ? {
                          id: 'qte_quick',
                          signature: 'signed',
                          expected_credits: 1,
                          cap_credits: 2,
                        }
                      : path === '/v1/runs'
                        ? { id: 'run_quick', status: 'queued' }
                        : { items: [] };
      return route.fulfill({ json: body, headers });
    });
    await page.route('**/storage/quick', (route) =>
      route.fulfill({
        status: 204,
        headers: {
          'Access-Control-Allow-Origin': '*',
          'Access-Control-Allow-Methods': 'PUT',
          'Access-Control-Allow-Headers': '*',
        },
      }),
    );
    await page.goto('/app');
    await expect(page.getByText(m.projects.paused).first()).toBeVisible();
    await expect(page.getByRole('button', { name: m.projects.new, exact: true })).toHaveCount(0);
    await page.getByRole('link', { name: /Fechamento salvo/ }).click();
    await expect(page.getByRole('heading', { name: project.name, exact: true })).toBeVisible();
    await expect(page.getByRole('textbox', { name: m.projects.name, exact: true })).toHaveCount(0);
    await expect(page.getByRole('tab', { name: m.projects.results, exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: m.projects.reconcile, exact: true })).toHaveCount(
      0,
    );
    expect((await new AxeBuilder({ page }).analyze()).violations.map((v) => v.id)).toEqual([]);
    await page.goto('/app');
    await page.getByRole('link', { name: m.projects.quick_run, exact: true }).click();
    await expect(page.locator('astro-island[component-export="HeroApp"]')).not.toHaveAttribute(
      'ssr',
    );
    await page.locator('input[type="file"]').setInputFiles({
      name: 'extrato.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from('id;valor\n1;10\n'),
    });
    await page.getByRole('button', { name: m.form.submit, exact: true }).click();
    const confirmation = page.getByRole('dialog');
    await expect(
      confirmation.getByText(
        m.projects.confirm_context
          .replace('{account}', m.projects.personal_account)
          .replace('{project}', m.projects.quick_run),
      ),
    ).toBeVisible();
    expect(writes.filter((write) => write.path === '/v1/runs')).toHaveLength(0);
    await confirmation.getByRole('button', { name: m.app.confirm_start, exact: true }).click();
    await expect(page).toHaveURL(/run=run_quick/);
    expect(writes.filter((write) => write.path === '/v1/runs')).toHaveLength(1);
    expect(writes.some((write) => write.path.startsWith('/v1/projects'))).toBe(false);
    expect(writes.every((write) => write.account === org.id)).toBe(true);
  });
}
