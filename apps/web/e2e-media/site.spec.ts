import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test, type Locator, type Page } from '@playwright/test';
import { m } from '../e2e/messages';

// Records the site for docs/media/site.gif and site.mp4 (tools/media/media.sh site): the public
// Vantara twin dropped on /app with the local sign-in, the estimate, the findings and the
// evidence drawer of one finding. Paced for people: every pause below exists for the video.
//
// Two things a Playwright video does not show are drawn into the page: the mouse pointer (a
// small arrow that follows the real mouse events) and the browser's native confirmation, which
// carries the estimate. window.confirm is replaced by a box with the very same text (and "OK"),
// and the run is held until the pointer has clicked it.
const twin = join(dirname(fileURLToPath(import.meta.url)), '../../../testdata/golden/vantara-twin');
const inputs = [
  '01_contas_a_receber_protheus_jul2026.csv',
  '02_titulos_unidade_norte_omie_jul2026.csv',
  '03_extrato_bancario_jul2026.csv',
  '04_caixa_de_entrada_contas_a_pagar.txt',
];

const pointer = String.raw`
  addEventListener('DOMContentLoaded', () => {
    const p = document.createElement('div');
    p.id = 'dm-media-pointer';
    p.innerHTML = '<svg width="22" height="22" viewBox="0 0 22 22"><path d="M3 2l14 9.5-6.2 1.1 3.6 7-2.6 1.3-3.6-7L3 18z" fill="#0F172A" stroke="#FFFFFF" stroke-width="1.4" stroke-linejoin="round"/></svg>';
    Object.assign(p.style, { position: 'fixed', left: '-40px', top: '-40px', zIndex: 2147483647, pointerEvents: 'none', transition: 'transform 80ms' });
    document.documentElement.append(p);
    addEventListener('mousemove', (e) => { p.style.left = e.clientX - 3 + 'px'; p.style.top = e.clientY - 2 + 'px'; }, true);
    addEventListener('mousedown', () => { p.style.transform = 'scale(0.85)'; }, true);
    addEventListener('mouseup', () => { p.style.transform = ''; }, true);
  });
  window.confirm = (message) => {
    const box = document.createElement('div');
    box.id = 'dm-media-confirm';
    Object.assign(box.style, { position: 'fixed', inset: '0', zIndex: 2147483646, background: 'rgba(15,23,42,.28)', display: 'flex', alignItems: 'flex-start', justifyContent: 'center', paddingTop: '72px', font: '15px/1.5 system-ui, sans-serif' });
    const card = document.createElement('div');
    Object.assign(card.style, { background: '#FFFFFF', color: '#0F172A', borderRadius: '12px', padding: '20px 22px 16px', width: '440px', boxShadow: '0 12px 40px rgba(15,23,42,.28)', whiteSpace: 'pre-line' });
    const text = document.createElement('p');
    text.style.margin = '0 0 16px';
    text.textContent = String(message);
    const row = document.createElement('div');
    Object.assign(row.style, { display: 'flex', justifyContent: 'flex-end', gap: '8px' });
    for (const [label, primary] of [['Cancelar', false], ['OK', true]]) {
      const b = document.createElement('button');
      b.textContent = label;
      if (primary) b.id = 'dm-media-ok';
      Object.assign(b.style, { font: 'inherit', padding: '6px 18px', borderRadius: '8px', border: '1px solid #9AA5B4', background: primary ? '#1D4ED8' : '#FFFFFF', color: primary ? '#FFFFFF' : '#0F172A' });
      row.append(b);
    }
    card.append(text, row);
    box.append(card);
    document.body.append(box);
    return true;
  };
`;

/** Moves the pointer to the middle of target in small steps, like a hand would. */
async function glide(page: Page, target: Locator, steps = 28): Promise<void> {
  await target.scrollIntoViewIfNeeded();
  const box = await target.boundingBox();
  if (!box) throw new Error('alvo sem caixa na tela');
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps });
}

async function clickLikeAHand(page: Page, target: Locator): Promise<void> {
  await glide(page, target);
  await page.waitForTimeout(250);
  await target.click();
}

test('the twin on the site, for the docs', async ({ page }) => {
  page.on('dialog', (dialog) => void dialog.accept()); // only if the replacement did not load
  await page.addInitScript(pointer);

  // Hold the run until the pointer has pressed "OK" on the estimate.
  let release: () => void = () => {};
  const clicked = new Promise<void>((resolve) => (release = resolve));
  await page.route('**/v1/runs', async (route) => {
    if (route.request().method() === 'POST') await clicked;
    await route.fallback();
  });

  await page.goto('/app');
  await page.mouse.move(600, 420);
  await page.waitForTimeout(1500);

  const input = page.locator('input[type="file"]');
  await glide(page, input.locator('xpath=..'), 30);
  await page.waitForTimeout(400);
  await input.setInputFiles(inputs.map((name) => join(twin, name)));
  await page.waitForTimeout(2200);

  await clickLikeAHand(page, page.getByRole('button', { name: m.form.submit }));

  const ok = page.locator('#dm-media-ok');
  await expect(ok).toBeVisible({ timeout: 120_000 });
  await page.waitForTimeout(2600);
  await glide(page, ok);
  await page.waitForTimeout(300);
  await page.mouse.down();
  await page.mouse.up();
  release();

  await page.waitForURL(/\/resultados\?run=/, { timeout: 120_000 });
  const rows = page.getByTestId('finding-row');
  await expect(rows.first()).toBeVisible({ timeout: 180_000 });
  await page.mouse.move(640, 300, { steps: 20 });
  await page.waitForTimeout(2500);
  for (let i = 0; i < 6; i++) {
    await page.mouse.wheel(0, 90);
    await page.waitForTimeout(140);
  }
  await page.waitForTimeout(1200);

  // The finding the installer also shows: a partial payment of exactly 50%.
  const half = rows.filter({ hasText: '50%' });
  const row = (await half.count()) > 0 ? half.first() : rows.first();
  await clickLikeAHand(page, row.getByRole('button').first());

  const drawer = page.getByRole('dialog');
  await expect(drawer).toBeVisible();
  await page.waitForTimeout(2000);
  // Point at the evidence: the bank line and the receivable it does not cover.
  await glide(page, drawer.getByText('LIQUIDACAO', { exact: false }).first(), 24);
  await page.waitForTimeout(2600);

  await clickLikeAHand(page, drawer.getByRole('button', { name: m.results.accept }));
  await expect(drawer.getByText(m.results.feedback_saved)).toBeVisible();
  await page.waitForTimeout(2500);
});
