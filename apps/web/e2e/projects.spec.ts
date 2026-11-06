import { AxeBuilder } from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { m } from './messages';

for (const viewport of [
  { width: 1440, height: 1000 },
  { width: 390, height: 844 },
  { width: 844, height: 390 },
]) {
  test(`project files expand without moving the action: ${viewport.width}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    const capabilities = {
      manage_account: true,
      create_project: true,
      reconcile: true,
      read_results: true,
      upload: true,
    };
    const org = {
      id: 'org_layout',
      name: 'Conta de teste',
      kind: 'personal',
      role: 'owner',
      plan: 'starter',
      capabilities,
    };
    const project = {
      id: 'prj_layout',
      name: 'Fechamento mensal',
      visibility: 'private',
      version: 1,
      revision: 1,
      document_count: 32,
      created_at: '2026-11-06T12:00:00Z',
      updated_at: '2026-11-06T12:00:00Z',
      archived_at: null,
      capabilities,
      settings: { use_rules: true, web_search: false },
      documents: Array.from({ length: 32 }, (_, index) => ({
        id: `doc_${index}`,
        upload_id: `upl_${index}`,
        file_name: `Documento ${index + 1}.csv`,
        role: '',
        status: index === 31 ? 'rejected' : 'ready',
        size_bytes: 512,
        created_at: '2026-11-06T12:00:00Z',
        removed_at: null,
      })),
    };
    await page.route('**/v1/**', async (route) => {
      const path = new URL(route.request().url()).pathname;
      const headers = {
        'Access-Control-Allow-Origin': route.request().headers()['origin'] ?? '*',
        'Access-Control-Allow-Credentials': 'true',
        'Access-Control-Allow-Headers': 'content-type,x-docmatch-org,idempotency-key',
        'Access-Control-Allow-Methods': 'GET,POST,PATCH,DELETE,PUT',
      };
      if (route.request().method() === 'OPTIONS') {
        await route.fulfill({ status: 204, headers });
        return;
      }
      const body =
        path === '/v1/me/organizations'
          ? { user_id: 'usr_layout', items: [org] }
          : path === '/v1/me'
            ? {
                user: { id: 'usr_layout', email: 'layout@example.invalid', display_name: 'Teste' },
                org,
              }
            : path === `/v1/projects/${project.id}`
              ? project
              : { items: [] };
      await route.fulfill({ json: body, headers });
    });
    await page.goto(`/app?project=${project.id}`);
    const action = page.getByRole('button', { name: m.projects.reconcile, exact: true });
    await expect(action).toBeInViewport({ ratio: 1 });
    await expect(page.getByText('Documento 1.csv', { exact: true })).toBeHidden();
    await expect(page.getByText(m.projects.files_attention)).toBeVisible();
    const before = await action.boundingBox();
    const disclosure = page.getByRole('button', {
      name: m.projects.show_files.replace('{count}', '32'),
    });
    await disclosure.focus();
    await page.keyboard.press('Enter');
    await expect(disclosure).toHaveCount(0);
    await expect(page.getByRole('button', { name: m.projects.hide_files })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    await page.getByText('Documento 32.csv', { exact: true }).scrollIntoViewIfNeeded();
    await expect(action).toBeInViewport({ ratio: 1 });
    expect(Math.abs((await action.boundingBox())!.y - before!.y)).toBeLessThan(2);
    await page.getByText(m.projects.manage, { exact: true }).click();
    await page
      .getByRole('button', { name: m.projects.delete, exact: true })
      .scrollIntoViewIfNeeded();
    const bar = page.getByRole('region', { name: m.projects.reconcile, exact: true });
    const last = await page
      .getByRole('button', { name: m.projects.delete, exact: true })
      .boundingBox();
    expect(last!.y + last!.height).toBeLessThanOrEqual((await bar.boundingBox())!.y);
    await page.evaluate(() => {
      document.documentElement.style.fontSize = '200%';
    });
    await page.locator('summary').filter({ hasText: m.projects.settings }).click();
    await page.locator('summary').filter({ hasText: m.projects.saved_rules }).click();
    await expect(action).toBeInViewport({ ratio: 1 });
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
      .toBe(true);
    const axe = await new AxeBuilder({ page }).analyze();
    expect(axe.violations.map(({ id }) => id)).toEqual([]);
  });
}
