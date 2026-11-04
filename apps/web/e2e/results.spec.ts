import { expect, test } from '@playwright/test';
import { m } from './messages';

function parseBrl(text: string): number {
  return Number.parseFloat(text.replace(/[^\d,-]/g, '').replace(',', '.'));
}

test('divergências vêm primeiro, ordenadas por valor em risco', async ({ page }) => {
  await page.goto('/resultados');
  const rows = page.getByTestId('finding-row');
  await expect(rows).toHaveCount(5);
  const amounts = await rows.locator('td:nth-child(2)').allTextContents();
  const values = amounts.map(parseBrl);
  expect(values).toEqual([...values].sort((a, b) => b - a));
  await expect(page.getByRole('button', { name: m.results.clean_show })).toBeVisible();
  await expect(page.locator('#limpas-lista')).toHaveCount(0);
});

test('a gaveta mostra a evidência lado a lado e registra a decisão', async ({ page }) => {
  await page.goto('/resultados');
  const opener = page.getByTestId('finding-row').first().getByRole('button');
  await opener.click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.locator('[data-cited="true"]').first()).toBeVisible();
  // One region per source file (the explanation/evidence sections are landmarks too).
  await expect(dialog.getByRole('region', { name: /\.(ofx|xlsx|pdf|m4a)$/ })).toHaveCount(2);
  await dialog.getByRole('button', { name: m.results.accept }).click();
  await expect(dialog.getByText(m.results.feedback_saved)).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(opener).toBeFocused();
  await expect(page.getByTestId('finding-row').first()).toHaveAttribute('data-status', 'accepted');
});

test('evidência em áudio abre um player posicionado no trecho citado', async ({ page }) => {
  await page.goto('/resultados');
  const row = page.getByTestId('finding-row').filter({ hasText: 'Acordo verbal' });
  await row.getByRole('button').click();
  const media = page.getByTestId('evidence-media');
  await expect(media).toHaveAttribute('src', /^blob:/);
  await expect
    .poll(() => media.evaluate((el) => (el as HTMLMediaElement).currentTime))
    .toBeGreaterThanOrEqual(1.9);
  await expect(
    page.getByText(m.results.evidence_media_at.replace('{time}', '00:02')),
  ).toBeVisible();
});

test('exportar CSV baixa um arquivo', async ({ page }) => {
  await page.goto('/resultados');
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', { name: m.results.export_csv }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe('docmatch-demo.csv');
});
