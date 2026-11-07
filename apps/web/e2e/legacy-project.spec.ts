import { AxeBuilder } from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { m } from './messages';

for (const width of [1280, 390]) {
  test(`save historical result in its original account without another run: ${width}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    const capabilities = {
      manage_account: true,
      create_project: true,
      reconcile: true,
      read_results: true,
      upload: true,
    };
    const org = {
      id: 'org_history',
      name: 'Empresa de exemplo',
      kind: 'company',
      role: 'owner',
      plan: 'starter',
      capabilities,
    };
    const old = {
      id: 'run_original',
      status: 'completed',
      sources: [],
      created_at: '2026-10-01T12:00:00Z',
      can_save_as_project: true,
    };
    const project = {
      id: 'prj_imported',
      imported_run_id: old.id,
      name: 'Outubro salvo',
      visibility: 'private',
      version: 1,
      revision: 1,
      document_count: 0,
      created_at: '2026-11-06T12:00:00Z',
      updated_at: '2026-11-06T12:00:00Z',
      archived_at: null,
      capabilities,
      settings: { use_rules: false, web_search: false },
      documents: [],
    };
    const writes: { path: string; account: string | undefined; body: unknown }[] = [];
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
        writes.push({
          path,
          account: request.headers()['x-docmatch-org'],
          body: request.postDataJSON(),
        });
      if (path === '/v1/projects/from-run' && writes.length === 1)
        return route.fulfill({
          status: 503,
          headers,
          json: { code: 'service_unavailable', message: 'Tente salvar novamente.' },
        });
      const body =
        path === '/v1/me/organizations'
          ? { user_id: 'usr_history', items: [org] }
          : path === '/v1/me'
            ? {
                user: { id: 'usr_history', email: 'history@example.test', display_name: 'Teste' },
                org,
              }
            : path === '/v1/projects/activity'
              ? { items: [old] }
              : path === '/v1/projects/from-run' || path === '/v1/projects/prj_imported'
                ? project
                : { items: [] };
      return route.fulfill({ json: body, headers });
    });
    await page.goto('/atividade?account=org_history');
    await page.locator('summary').filter({ hasText: m.projects.import_action }).click();
    await expect(
      page.getByText(m.projects.import_explanation.replace('{account}', org.name)),
    ).toBeVisible();
    await page.getByRole('textbox', { name: m.projects.name, exact: true }).fill(project.name);
    await page.getByRole('button', { name: m.projects.import_action, exact: true }).click();
    await expect(page.getByRole('alert')).toHaveText('Tente salvar novamente.');
    await expect(page.getByRole('textbox', { name: m.projects.name, exact: true })).toHaveValue(
      project.name,
    );
    expect((await new AxeBuilder({ page }).analyze()).violations.map((v) => v.id)).toEqual([]);
    await page.getByRole('button', { name: m.projects.import_action, exact: true }).click();
    await expect(page).toHaveURL(/\/app\?account=org_history&project=prj_imported/);
    await expect(page.getByText(m.projects.imported_notice)).toBeVisible();
    await expect(
      page.getByRole('link', { name: m.projects.imported_result, exact: true }),
    ).toHaveAttribute(
      'href',
      '/resultados?account=org_history&project=prj_imported&run=run_original',
    );
    expect(writes).toEqual(
      [1, 2].map(() => ({
        path: '/v1/projects/from-run',
        account: org.id,
        body: { run_id: old.id, name: project.name },
      })),
    );
    expect((await new AxeBuilder({ page }).analyze()).violations.map((v) => v.id)).toEqual([]);
  });
}
