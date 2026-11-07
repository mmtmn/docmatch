import { AxeBuilder } from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { m } from './messages';

for (const width of [1280, 390]) {
  test(`invitation inbox preserves the selected account: ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const capabilities = {
      manage_account: true,
      create_project: true,
      reconcile: true,
      read_results: true,
      upload: true,
    };
    const personal = {
      id: 'org_personal',
      name: 'Pessoal',
      kind: 'personal',
      role: 'owner',
      plan: 'starter',
      capabilities,
    };
    const company = {
      ...personal,
      id: 'org_company',
      name: 'Escritório convidante',
      kind: 'team',
      role: 'uploader',
    };
    let accepted = false;
    let posts = 0;
    await page.route('**/v1/**', async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      const headers = {
        'Access-Control-Allow-Origin': request.headers()['origin'] ?? '*',
        'Access-Control-Allow-Credentials': 'true',
        'Access-Control-Allow-Headers': 'content-type,x-docmatch-org,idempotency-key',
        'Access-Control-Allow-Methods': 'GET,POST',
      };
      if (request.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
      if (path === '/v1/me/invitations/inv_inbox/respond') {
        posts++;
        expect(request.headers()['x-docmatch-org']).toBeUndefined();
        expect(request.postDataJSON()).toEqual({ accept: true });
        accepted = true;
        return route.fulfill({ json: { accepted, organization_id: company.id }, headers });
      }
      const body =
        path === '/v1/me/organizations'
          ? { user_id: 'usr_inbox', items: accepted ? [personal, company] : [personal] }
          : path === '/v1/me'
            ? {
                user: { id: 'usr_inbox', email: 'inbox@example.test', display_name: 'Teste' },
                org: personal,
              }
            : path === '/v1/me/invitations'
              ? {
                  items: accepted
                    ? []
                    : [
                        {
                          id: 'inv_inbox',
                          organization: company.name,
                          role: 'uploader',
                          status: 'pending',
                          expires_at: '2026-11-13T12:00:00Z',
                          email_hint: 'i***@example.test',
                          email_matches: true,
                        },
                      ],
                }
              : { items: [] };
      await route.fulfill({ json: body, headers });
    });
    await page.goto('/convites');
    await expect(page.getByRole('heading', { name: company.name })).toBeVisible();
    const selector = page.getByRole('combobox', { name: m.projects.choose_account });
    await expect(selector).toHaveValue(personal.id);
    await expect(
      page.getByRole('link', { name: m.teams.inbox_link.replace('{count}', '1') }),
    ).toBeVisible();
    expect(posts).toBe(0);
    const accept = page.getByRole('button', { name: m.teams.accept_invitation, exact: true });
    await accept.focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('dialog')).toBeVisible();
    expect(posts).toBe(0);
    expect((await new AxeBuilder({ page }).analyze()).violations.map((v) => v.id)).toEqual([]);
    await page.getByRole('button', { name: m.common.cancel, exact: true }).click();
    await expect(accept).toBeFocused();
    await accept.click();
    await page.getByRole('button', { name: m.common.confirm, exact: true }).click();
    await expect(page.getByText(m.teams.inbox_accepted, { exact: true })).toBeVisible();
    expect(posts).toBe(1);
    await expect(selector).toHaveValue(personal.id);
    await expect(selector.locator('option')).toHaveCount(2);
    await expect(
      page.getByRole('link', { name: m.teams.inbox_link.replace('{count}', '1') }),
    ).toHaveCount(0);
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
      .toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.map((v) => v.id)).toEqual([]);
  });
}
