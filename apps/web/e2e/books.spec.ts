import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('search waits for its script before accepting the first click', async ({ page }) => {
  let releaseScript!: () => void;
  const scriptReady = new Promise<void>((resolve) => {
    releaseScript = resolve;
  });
  await page.route('**/docs/usar/searcher-*.js', async (route) => {
    await scriptReady;
    await route.continue();
  });
  try {
    await page.goto('/docs/usar/', { waitUntil: 'commit' });
    const toggle = page.getByRole('button', { name: 'Abrir ou fechar a busca' });
    await expect(toggle).toBeVisible();
    await expect(toggle).toBeDisabled();
    releaseScript();
    await expect(toggle).toBeEnabled();
    await toggle.click();
    await page.getByRole('searchbox', { name: 'Buscar neste guia' }).fill('créditos');
    await expect(page.locator('#mdbook-searchresults li').first()).toBeVisible();
  } finally {
    releaseScript();
  }
});

test('both guides load from the site with strict CSP, deep links and Portuguese search', async ({
  page,
}) => {
  const violations: string[] = [];
  await page.addInitScript(() => {
    window.addEventListener('securitypolicyviolation', (event) => {
      console.error(`BOOK_CSP ${event.violatedDirective} ${event.blockedURI}`);
    });
  });
  page.on('console', (message) => {
    if (message.text().includes('BOOK_CSP')) violations.push(message.text());
  });
  await page.goto('/docs');
  await page.getByRole('link', { name: 'Começar meu primeiro projeto' }).click();
  await expect(page.locator('main h1')).toHaveText('Usar a docmatch');
  await page.getByRole('button', { name: 'Abrir ou fechar a busca' }).click();
  await page.getByRole('searchbox', { name: 'Buscar neste guia' }).fill('créditos');
  await expect(page.locator('#mdbook-searchresults li').first()).toBeVisible();
  await expect(page.locator('#mdbook-searchresults-header')).toContainText('resultado');
  await page.locator('#mdbook-searchresults a').first().click();
  await expect(page.locator('main')).toContainText('créditos');
  await page.goto('/docs/desenvolver/python');
  await expect(page.locator('main h1')).toHaveText('Python');
  await expect(page.locator('main pre').last()).toContainText('DOCMATCH_APPROVE_CAP');
  await page.locator('main pre code').first().focus();
  await expect(page.getByRole('button', { name: 'Copiar código' }).first()).toBeVisible();
  await page.getByRole('link', { name: 'Usar a docmatch', exact: true }).click();
  await expect(page).toHaveURL(/\/docs\/usar\/$/);
  expect(violations).toEqual([]);
});

test('book roots redirect without losing search and every chapter and sample is present', async ({
  request,
}) => {
  for (const guide of ['usar', 'desenvolver']) {
    const redirect = await request.get(`/docs/${guide}?search=projeto`, { maxRedirects: 0 });
    expect(redirect.status()).toBe(301);
    expect(redirect.headers()['location']).toBe(`/docs/${guide}/?search=projeto`);
    const index = await request.get(`/docs/${guide}/`);
    expect(index.ok()).toBe(true);
    const html = await index.text();
    expect(html).toContain('Content-Security-Policy');
    expect(html).not.toContain('unsafe-inline');
    expect(html).not.toContain('<iframe');
    const toc = await request.get(`/docs/${guide}/toc.html`);
    const links = [...(await toc.text()).matchAll(/href="([^"#]+\.html)"/g)].map(
      (match) => match[1],
    );
    expect(links.length).toBeGreaterThan(5);
    for (const link of new Set(links)) {
      const response = await request.get(`/docs/${guide}/${link}`);
      expect(response.ok(), link).toBe(true);
      expect(await response.text(), link).toContain('<main id="mdbook-main"');
    }
    const csv = await request.get(`/docs/${guide}/assets/examples/cobrancas.csv`);
    expect(csv.ok()).toBe(true);
    expect(await csv.text()).toContain('Cliente Exemplo A');
  }
  const api = await request.get('/docs/desenvolver/assets/openapi.json');
  const spec = (await api.json()) as { paths: Record<string, unknown> };
  expect(spec.paths['/projects']).toBeDefined();
  expect(Object.keys(spec.paths).some((route) => route.startsWith('/admin'))).toBe(false);
});

test('guides remain readable on a small screen with search and keyboard navigation', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  for (const chapter of ['/docs/usar/primeiro-projeto.html', '/docs/desenvolver/python.html']) {
    await page.goto(chapter);
    await expect(page.locator('main h1')).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true);
    const scan = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
      .analyze();
    expect(scan.violations).toEqual([]);
    await page.locator('#mdbook-sidebar-toggle').click();
    await expect(page.locator('#mdbook-sidebar')).toHaveAttribute('aria-hidden', 'false');
    await page.locator('#mdbook-sidebar-toggle').click();
    await page.getByRole('button', { name: 'Abrir ou fechar a busca' }).click();
    await page.getByRole('searchbox', { name: 'Buscar neste guia' }).fill('projeto');
    await expect(page.locator('#mdbook-searchresults li').first()).toBeVisible();
    await page.keyboard.press('Escape');
  }
});

test('chapters, the index and next-page links work without JavaScript', async ({
  browser,
  baseURL,
}) => {
  if (!baseURL) throw new Error('Book tests require the site preview URL');
  const context = await browser.newContext({
    javaScriptEnabled: false,
    baseURL,
    ignoreHTTPSErrors: true,
  });
  const page = await context.newPage();
  try {
    await page.goto('/docs/usar/');
    await expect(page.locator('main h1')).toHaveText('Usar a docmatch');
    await page.locator('summary').filter({ hasText: 'Índice' }).click();
    await page.getByRole('link', { name: 'Experimente com dois arquivos', exact: true }).click();
    await expect(page.locator('main h1')).toHaveText('Experimente com dois arquivos');
    await expect(page.getByRole('link', { name: 'Próximo capítulo' }).first()).toBeAttached();
  } finally {
    await context.close();
  }
});
