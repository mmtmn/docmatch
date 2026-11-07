import { AxeBuilder } from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { m } from './messages';

for (const width of [1280, 390]) {
  test(`recovers offline edits only after review and protects intervening changes: ${width}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    page.on('dialog', (dialog) => void dialog.accept());
    const capabilities = {
      manage_account: true,
      create_project: true,
      reconcile: true,
      read_results: true,
      upload: true,
    };
    const org = {
      id: 'org_recovery',
      name: 'Conta de teste',
      kind: 'personal',
      role: 'owner',
      plan: 'starter',
      capabilities,
    };
    let project = {
      id: 'prj_recovery',
      name: 'Na conta originalmente',
      visibility: 'private',
      version: 1,
      revision: 1,
      document_count: 0,
      created_at: '2026-11-06T12:00:00Z',
      updated_at: '2026-11-06T12:00:00Z',
      archived_at: null,
      capabilities,
      settings: { use_rules: true, web_search: false },
      documents: [],
    };
    let offline = true;
    const patches: { version: number; name: string }[] = [];
    await page.route('**/v1/**', async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      const headers = {
        'Access-Control-Allow-Origin': request.headers()['origin'] ?? '*',
        'Access-Control-Allow-Credentials': 'true',
        'Access-Control-Allow-Headers': 'content-type,x-docmatch-org,idempotency-key',
        'Access-Control-Allow-Methods': 'GET,POST,PATCH,DELETE,PUT',
      };
      if (request.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
      if (request.method() === 'PATCH') {
        expect(request.headers()['x-docmatch-org']).toBe(org.id);
        const patch = request.postDataJSON() as { version: number; name: string };
        patches.push(patch);
        if (offline) return route.abort();
        if (patch.version !== project.version)
          return route.fulfill({
            status: 409,
            headers,
            json: { code: 'project_changed', message: 'Changed' },
          });
        project = { ...project, name: patch.name, version: project.version + 1 };
        return route.fulfill({ json: project, headers });
      }
      const body =
        path === '/v1/me/organizations'
          ? { user_id: 'usr_recovery', items: [org] }
          : path === '/v1/me'
            ? {
                user: { id: 'usr_recovery', email: 'recovery@example.test', display_name: 'Teste' },
                org,
              }
            : path === `/v1/projects/${project.id}`
              ? project
              : { items: [] };
      return route.fulfill({ json: body, headers });
    });
    await page.goto(`/app?project=${project.id}`);
    const name = page.getByRole('textbox', { name: m.projects.name, exact: true });
    await name.fill('Meu texto sem conexão');
    await expect(page.getByText(m.projects.recovery_stored, { exact: true })).toBeVisible();
    const beforeBlur = patches.length;
    await name.press('Tab');
    await expect.poll(() => patches.length).toBe(beforeBlur + 1);
    await expect(page.getByText(m.projects.recovery_stored, { exact: true })).toBeVisible();
    const failedWrites = patches.length;
    offline = false;
    project = { ...project, name: 'Atualizado por uma colega', version: 2 };
    await page.reload();
    await expect(name).toHaveValue(project.name);
    const disclosure = page
      .locator('summary')
      .filter({ hasText: m.projects.recovery_title.replace('{count}', '1') });
    await expect(disclosure).toBeVisible();
    await disclosure.click();
    await expect(page.getByText(m.projects.recovery_changed, { exact: true })).toBeVisible();
    await expect(page.getByText('Meu texto sem conexão', { exact: true })).toBeVisible();
    expect((await new AxeBuilder({ page }).analyze()).violations.map((v) => v.id)).toEqual([]);
    expect(patches).toHaveLength(failedWrites);
    const apply = page.getByRole('button', { name: m.projects.recovery_apply, exact: true });
    await apply.focus();
    await page.keyboard.press('Enter');
    await page.getByRole('button', { name: m.common.cancel, exact: true }).click();
    await expect(apply).toBeFocused();
    expect(patches).toHaveLength(failedWrites);

    // A change after the comparison was opened must require another review.
    await apply.click();
    project = { ...project, name: 'Outra alteração recente', version: 3 };
    await page.getByRole('button', { name: m.common.confirm, exact: true }).click();
    await expect(page.getByText(m.projects.recovery_refresh, { exact: true })).toBeVisible();
    await expect(name).toHaveValue(project.name);
    expect(patches).toHaveLength(failedWrites);
    await expect(apply).toBeEnabled();
    await apply.click();
    await page.getByRole('button', { name: m.common.confirm, exact: true }).click();
    await expect(name).toHaveValue('Meu texto sem conexão');
    await expect(disclosure).toHaveCount(0);
    expect(patches.at(-1)).toEqual({ version: 3, name: 'Meu texto sem conexão' });
    expect(patches).toHaveLength(failedWrites + 1);
    await page.reload();
    await expect(name).toHaveValue('Meu texto sem conexão');
    await expect(disclosure).toHaveCount(0);
    expect((await new AxeBuilder({ page }).analyze()).violations.map((v) => v.id)).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
  });
}
