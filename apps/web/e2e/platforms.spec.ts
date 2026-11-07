import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('platform actions wait for hydration before accepting the first click', async ({ page }) => {
  let releaseScript!: () => void;
  const scriptReady = new Promise<void>((resolve) => {
    releaseScript = resolve;
  });
  await page.route('**/_astro/PlatformLinks.*.js', async (route) => {
    await scriptReady;
    await route.continue();
  });
  try {
    await page.goto('/', { waitUntil: 'commit' });
    const section = page.getByRole('region', { name: 'Use onde preferir' }).first();
    const platform = section.getByRole('button', { name: 'iOS Em breve', exact: true });
    await expect(platform).toBeVisible();
    await expect(platform).toBeDisabled();
    releaseScript();
    await expect(platform).toBeEnabled();
    await platform.click();
    await expect(section.getByRole('status')).toContainText('Quer receber um aviso por e-mail?');
  } finally {
    releaseScript();
  }
});

for (const width of [1280, 390]) {
  test(`platform notice asks for consent and restores focus at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 });
    const requests: unknown[] = [];
    await page.route('**/v1/**', async (route) => {
      const request = route.request();
      const headers = {
        'Access-Control-Allow-Origin': request.headers()['origin'] ?? '*',
        'Access-Control-Allow-Credentials': 'true',
        'Access-Control-Allow-Headers': 'content-type',
        'Access-Control-Allow-Methods': 'GET,POST',
      };
      if (request.method() === 'OPTIONS') {
        await route.fulfill({ status: 204, headers });
        return;
      }
      if (new URL(request.url()).pathname === '/v1/platform-notices') {
        requests.push(request.postDataJSON());
        await route.fulfill({ status: 202, headers, json: { accepted: true } });
        return;
      }
      await route.fulfill({ status: 401, headers, json: { code: 'unauthenticated' } });
    });
    await page.goto('/');
    const section = page.getByRole('region', { name: 'Use onde preferir' }).first();
    const platform = section.getByRole('button', { name: 'iOS Em breve', exact: true });
    await expect(platform).toBeEnabled();
    await platform.focus();
    await platform.press('Enter');
    await expect(platform).toBeFocused();
    await expect(section.getByRole('status')).toContainText('Quer receber um aviso por e-mail?');
    const open = section.getByRole('button', { name: 'Avisar-me', exact: true });
    await open.click();
    let dialog = page.getByRole('dialog', { name: 'Avise-me sobre iOS' });
    await expect(dialog).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(open).toBeFocused();
    await open.click();
    dialog = page.getByRole('dialog', { name: 'Avise-me sobre iOS' });
    await dialog.getByRole('textbox', { name: 'Seu e-mail' }).fill('reader@example.test');
    await dialog.getByRole('button', { name: 'Enviar confirmação' }).click();
    expect(requests).toHaveLength(0);
    await dialog.getByRole('checkbox').check();
    const scan = await new AxeBuilder({ page })
      .include('dialog')
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
      .analyze();
    expect(scan.violations).toEqual([]);
    await dialog.getByRole('button', { name: 'Enviar confirmação' }).click();
    await expect(dialog.getByRole('status')).toContainText('Confira seu e-mail');
    expect(requests).toEqual([{ platform: 'ios', email: 'reader@example.test', consent: true }]);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true);
  });
}
for (const [key, action, label, done] of [
  ['confirmar', 'confirm', 'Confirmar meu aviso', 'Pedido confirmado.'],
  ['cancelar', 'unsubscribe', 'Cancelar meu aviso', 'Pedido cancelado.'],
] as const) {
  test(`email ${action} link needs an explicit action and clears its token`, async ({ page }) => {
    let posts = 0;
    const token = 'A'.repeat(action === 'confirm' ? 43 : 76);
    await page.route('**/v1/platform-notices/**', async (route) => {
      const request = route.request();
      const headers = {
        'Access-Control-Allow-Origin': request.headers()['origin'] ?? '*',
        'Access-Control-Allow-Credentials': 'true',
        'Access-Control-Allow-Headers': 'content-type',
        'Access-Control-Allow-Methods': 'POST',
      };
      if (request.method() === 'OPTIONS') {
        await route.fulfill({ status: 204, headers });
        return;
      }
      posts++;
      expect(request.url()).toContain(`/platform-notices/${action}`);
      expect(request.postDataJSON()).toEqual({ token });
      await route.fulfill({ status: 200, headers, json: { status: 'confirmed', platform: 'ios' } });
    });
    await page.goto(`/avisos#${key}=${token}`);
    const submit = page.getByRole('button', { name: label, exact: true });
    await expect(submit).toBeVisible();
    await expect(page).toHaveURL(/\/avisos$/);
    expect(posts).toBe(0);
    await submit.click();
    await expect(page.getByRole('status').filter({ hasText: done })).toBeVisible();
    expect(posts).toBe(1);
    if (action === 'confirm') {
      // Email apps may reuse the confirmation tab for the later cancellation link.
      const cancelled = 'B'.repeat(76);
      await page.goto(`/avisos#cancelar=${cancelled}`);
      await expect(
        page.getByRole('button', { name: 'Cancelar meu aviso', exact: true }),
      ).toBeVisible();
      await expect(page).toHaveURL(/\/avisos$/);
      expect(posts).toBe(1);
    }
  });
}
