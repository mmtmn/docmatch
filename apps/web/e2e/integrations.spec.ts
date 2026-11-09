import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

for (const width of [1280, 390]) {
  test(`integration interest is confirmed only after saving at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const requests: { integration: string; visitor_id: string }[] = [];
    let fail = true;
    await page.route('**/v1/**', async (route) => {
      const request = route.request();
      const headers = {
        'Access-Control-Allow-Origin': request.headers()['origin'] ?? '*',
        'Access-Control-Allow-Credentials': 'true',
        'Access-Control-Allow-Headers': 'content-type',
        'Access-Control-Allow-Methods': 'GET,POST',
      };
      if (request.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
      if (new URL(request.url()).pathname === '/v1/integration-interests') {
        requests.push(request.postDataJSON() as { integration: string; visitor_id: string });
        if (fail) {
          fail = false;
          return route.fulfill({ status: 503, headers, json: { code: 'service_unavailable' } });
        }
        return route.fulfill({
          status: 202,
          headers,
          json: { accepted: true, status: 'coming_soon' },
        });
      }
      return route.fulfill({ status: 401, headers, json: { code: 'unauthenticated' } });
    });
    await page.goto('/');
    const open = page.getByRole('button', { name: 'Integrações', exact: true });
    await expect(open).toBeEnabled();
    await open.click();
    const dialog = page.getByRole('dialog', { name: 'Seu trabalho, conectado.' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('button', { name: /^Integrar / })).toHaveCount(10);
    expect(requests).toHaveLength(0);
    expect((await new AxeBuilder({ page }).include('dialog').analyze()).violations).toEqual([]);
    await dialog.getByRole('button', { name: 'Contabilidade', exact: true }).click();
    await expect(dialog.getByRole('button', { name: /^Integrar / })).toHaveCount(2);
    await dialog.getByRole('button', { name: 'Todas', exact: true }).click();
    const action = dialog.getByRole('button', { name: 'Integrar Conta Azul', exact: true });
    await action.click();
    await expect(dialog.getByRole('status')).toContainText('Não foi possível');
    await expect(action).toBeEnabled();
    await action.click();
    await expect(
      dialog.getByRole('button', { name: 'Em breve Conta Azul', exact: true }),
    ).toBeDisabled();
    await expect(dialog.getByRole('status')).toContainText('Seu interesse foi registrado.');
    expect(requests).toHaveLength(2);
    expect(requests[0]).toEqual(requests[1]);
    expect(requests[0]?.visitor_id).toMatch(/^[0-9a-f-]{36}$/);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await page.screenshot({
      path: `../../scratch/integrations-${width}-${test.info().project.name}.png`,
    });
    await page.keyboard.press('Escape');
    await expect(open).toBeFocused();
    await open.click();
    await expect(
      dialog.getByRole('button', { name: 'Em breve Conta Azul', exact: true }),
    ).toBeDisabled();
  });
}
