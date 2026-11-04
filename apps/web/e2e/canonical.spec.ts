import { expect, test, type Page } from '@playwright/test';

// build.format 'file' writes /termos as termos.html. The canonical URL (and og:url) must be the
// clean URL the sitemap lists, the same one CloudFront and tools/serve-dist.mjs map to the file.
const pages = ['/', '/termos', '/blog'];

function locs(xml: string): string[] {
  return Array.from(xml.matchAll(/<loc>([^<]+)<\/loc>/g), (match) => match[1]!);
}

/** Every URL the sitemap index and its chunks list, fetched from the server under test. */
async function sitemapUrls(page: Page): Promise<string[]> {
  const indexHref = await page.locator('link[rel="sitemap"]').getAttribute('href');
  expect(indexHref).not.toBeNull();
  const index = await page.request.get(indexHref!);
  expect(index.ok()).toBe(true);
  const urls: string[] = [];
  for (const chunk of locs(await index.text())) {
    const response = await page.request.get(new URL(chunk).pathname);
    expect(response.ok()).toBe(true);
    urls.push(...locs(await response.text()));
  }
  return urls;
}

for (const path of pages) {
  test(`${path}: o canonical é a URL limpa que o sitemap lista`, async ({ page }) => {
    await page.goto(path);
    const canonical = page.locator('link[rel="canonical"]');
    await expect(canonical).toHaveCount(1);
    const href = await canonical.getAttribute('href');
    expect(href).not.toBeNull();
    expect(new URL(href!).pathname).toBe(path);
    expect(href).not.toMatch(/\.html$/);
    await expect(page.locator('meta[property="og:url"]')).toHaveAttribute('content', href!);
    expect(await sitemapUrls(page)).toContain(href);
  });
}
