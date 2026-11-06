import { AxeBuilder } from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';
import { m } from './messages';

const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'];
const pages = ['/', '/app', '/termos', '/privacidade', '/contato', '/blog', '/resultados'];

async function expectNoViolations(page: Page): Promise<void> {
  const results = await new AxeBuilder({ page }).withTags(TAGS).analyze();
  const summary = results.violations.map((violation) => ({
    id: violation.id,
    impact: violation.impact,
    nodes: violation.nodes.map((node) => node.target.join(' ')),
  }));
  expect(summary).toEqual([]);
}

for (const path of pages) {
  test(`axe: zero violações em ${path}`, async ({ page }) => {
    await page.goto(path);
    await expectNoViolations(page);
  });
}

test('axe: zero violações com a gaveta do achado aberta', async ({ page }) => {
  await page.goto('/resultados');
  await page.getByText(m.results.all_heading, { exact: true }).click();
  await page.getByTestId('finding-row').first().getByRole('button').click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expectNoViolations(page);
});

test('axe: zero violações com a dica e o seletor abertos', async ({ page }) => {
  await page.goto('/app');
  await page.getByRole('button', { name: m.tooltips.more_info }).first().click();
  await expect(page.getByRole('tooltip').first()).toBeVisible();
  await page.getByRole('button', { name: /Regras salvas/ }).click();
  await expect(page.getByRole('dialog', { name: m.picker.label })).toBeVisible();
  await expectNoViolations(page);
});
