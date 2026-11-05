import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test } from '@playwright/test';
import { m } from '../e2e/messages';

// The public Vantara twin: the same traps and the same scorecard as the private case.
const twin = join(dirname(fileURLToPath(import.meta.url)), '../../../testdata/golden/vantara-twin');
const inputs = [
  '01_contas_a_receber_protheus_jul2026.csv',
  '02_titulos_unidade_norte_omie_jul2026.csv',
  '03_extrato_bancario_jul2026.csv',
  '04_caixa_de_entrada_contas_a_pagar.txt',
];
// Entries of the `findings:` section of the answer key (one `  - { code: Fnn, ... }` line each).
function expectedFindings(): number {
  const yaml = readFileSync(join(twin, 'expected.yaml'), 'utf8');
  const section = yaml.split(/^findings:\s*$/m)[1]?.split(/^[a-z_]+:/m)[0] ?? '';
  return (section.match(/^ {2}- \{ code: F\d+/gm) ?? []).length;
}

test('reconciles the twin with no key and no sign-in step', async ({ page }) => {
  page.on('dialog', (dialog) => void dialog.accept()); // the estimate confirmation
  await page.goto('/app');
  await page.locator('input[type="file"]').setInputFiles(inputs.map((name) => join(twin, name)));
  await page.getByRole('button', { name: m.form.submit }).click();

  await page.waitForURL(/\/resultados\?run=/, { timeout: 120_000 });
  const rows = page.getByTestId('finding-row');
  await expect(rows.first()).toBeVisible({ timeout: 150_000 });
  await expect(rows).toHaveCount(expectedFindings());

  // The decision goes to the API and the account page shows this browser as a device.
  await rows.first().getByRole('button').click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: m.results.accept }).click();
  await expect(dialog.getByText(m.results.feedback_saved)).toBeVisible();

  await page.goto('/conta');
  await expect(page.getByText('dev@docmatch.test')).toBeVisible();
  await expect(page.getByText(m.account.this_device, { exact: true })).toBeVisible();
});
