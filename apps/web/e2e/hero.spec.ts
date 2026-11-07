import { expect, test, type Locator, type Page } from '@playwright/test';
import { m } from './messages';

const viewports = [
  { width: 360, height: 640 },
  { width: 390, height: 844 },
  { width: 768, height: 1024 },
  { width: 1366, height: 768 },
  { width: 2560, height: 1440 },
];
const lineCounts = [0, 6, 40];

async function fillRules(page: Page, lines: number): Promise<Locator> {
  const textarea = page.getByLabel(m.form.rules_label);
  // Hydration done: the autosize hook has applied a pixel height.
  await expect.poll(() => textarea.evaluate((el) => el.style.height)).toMatch(/px$/);
  if (lines > 0) {
    await textarea.fill(Array.from({ length: lines }, (_, i) => `regra ${i + 1}`).join('\n'));
  }
  return textarea;
}

async function documentScroll(page: Page): Promise<{ scrollHeight: number; clientHeight: number }> {
  return page.evaluate(() => {
    const el = document.scrollingElement!;
    return { scrollHeight: el.scrollHeight, clientHeight: el.clientHeight };
  });
}

async function textareaMetrics(textarea: Locator) {
  return textarea.evaluate((el) => {
    const style = getComputedStyle(el);
    return {
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
      overflowY: style.overflowY,
      lineHeight: Number.parseFloat(style.lineHeight),
      height: el.getBoundingClientRect().height,
    };
  });
}

for (const viewport of viewports) {
  for (const lines of lineCounts) {
    test(`/ em ${viewport.width}×${viewport.height} com ${lines} linha(s): o formulário cabe na primeira tela`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.goto('/');
      const textarea = await fillRules(page, lines);

      const firstScreen = (await page.locator('.first-screen').boundingBox())!;
      expect(firstScreen.y + firstScreen.height).toBeLessThanOrEqual(viewport.height + 0.5);
      expect(await page.evaluate(() => window.scrollY)).toBe(0);

      const ta = await textareaMetrics(textarea);
      // Rules now grow into the drop area up to the viewport budget (not a fixed six lines).
      // Small screens may scroll the textarea even with six lines; the first screen must stay within its viewport.
      expect(ta.height).toBeLessThanOrEqual(Math.min(viewport.height * 0.28, 256) + 1);
      if (ta.scrollHeight > ta.clientHeight + 1) {
        expect(ta.overflowY).toBe('auto');
      } else {
        expect(ta.overflowY).toBe('hidden');
      }

      const button = page.getByRole('button', { name: m.form.submit });
      await expect(button).toBeVisible();
      const box = (await button.boundingBox())!;
      expect(box.y).toBeGreaterThanOrEqual(0);
      expect(box.y + box.height).toBeLessThanOrEqual(viewport.height + 0.5);
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 0.5);
      expect(box.height).toBeGreaterThanOrEqual(44);
    });
  }

  test(`/ em ${viewport.width}×${viewport.height}: regras crescem para cima e reduzem a área de arquivos`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto('/');
    const textarea = await fillRules(page, 1);
    const drop = page.locator('[data-drop-area]');
    const beforeRules = (await textarea.boundingBox())!;
    const beforeDrop = (await drop.boundingBox())!;
    await fillRules(page, 40);
    await expect
      .poll(async () => (await textarea.boundingBox())!.height)
      .toBeGreaterThan(beforeRules.height);
    const afterRules = (await textarea.boundingBox())!;
    const afterDrop = (await drop.boundingBox())!;
    expect(afterRules.y).toBeLessThan(beforeRules.y);
    expect(afterDrop.height).toBeLessThan(beforeDrop.height);
    expect(afterDrop.height).toBeGreaterThanOrEqual(44);
    expect(
      Math.abs(afterRules.y + afterRules.height - beforeRules.y - beforeRules.height),
    ).toBeLessThanOrEqual(1);
  });

  test(`/ em ${viewport.width}×${viewport.height}: o herói cabe na primeira tela e a rolagem começa depois`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto('/');
    await fillRules(page, 40);
    const hero = (await page.getByTestId('hero').boundingBox())!;
    expect(hero.y + hero.height).toBeLessThanOrEqual(viewport.height + 0.5);
    const button = (await page.getByRole('button', { name: m.form.submit }).boundingBox())!;
    expect(button.y + button.height).toBeLessThanOrEqual(viewport.height + 0.5);
    expect(await page.evaluate(() => window.scrollY)).toBe(0);
    const doc = await documentScroll(page);
    expect(doc.scrollHeight).toBeGreaterThan(doc.clientHeight);
  });
}

test('a primeira tela segue a ordem título, subtítulo, arquivos, regras, botão', async ({
  page,
}) => {
  await page.goto('/');
  const hero = page.getByTestId('hero');
  await expect(hero).toHaveCSS('display', 'grid');
  const rows = await hero.evaluate((el) => getComputedStyle(el).gridTemplateRows.split(' ').length);
  expect(rows).toBe(5);
  const order = await hero.evaluate((el) =>
    Array.from(el.children).map((child) => child.tagName.toLowerCase()),
  );
  expect(order).toEqual(['h1', 'p', 'div', 'div', 'div']);
});

test('a CSP gerada não bloqueia nada na primeira tela', async ({ page }) => {
  const violations: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error') violations.push(message.text());
  });
  page.on('pageerror', (error) => violations.push(error.message));
  await page.goto('/');
  await fillRules(page, 3);
  await expect(page.locator('meta[http-equiv="content-security-policy"]')).toHaveCount(1);
  expect(violations.filter((text) => /content security policy|refused/i.test(text))).toEqual([]);
});

test('arquivos entram pelo seletor, viram fichas e o rascunho sobrevive ao recarregar', async ({
  page,
}) => {
  await page.goto('/');
  await fillRules(page, 1);
  await page.locator('input[type="file"]').setInputFiles({
    name: 'nota.pdf',
    mimeType: 'application/pdf',
    buffer: Buffer.from('%PDF-1.7\n1 0 obj\n<<>>\nendobj\n'),
  });
  await expect(page.getByText('nota.pdf')).toBeVisible();
  await expect(page.getByRole('button', { name: m.form.submit })).not.toHaveAttribute(
    'aria-disabled',
    'true',
  );
  await expect(page.getByText(/tokens \(máx\./)).toBeVisible();

  await page.locator('input[type="file"]').setInputFiles({
    name: 'virus.exe',
    mimeType: 'application/octet-stream',
    buffer: Buffer.from([0x4d, 0x5a, 0x00, 0x00, 0x01]),
  });
  await expect(page.getByText(/virus\.exe: formato não reconhecido/)).toBeVisible();

  await page.waitForTimeout(900); // debounce of the encrypted draft
  await page.reload();
  await expect(page.getByText(m.form.draft_restored)).toBeVisible();
  await expect(page.getByText('nota.pdf')).toBeVisible();
  await expect(page.getByLabel(m.form.rules_label)).toHaveValue('regra 1');
  await page.getByRole('button', { name: m.form.draft_clear }).click();
  await expect(page.getByText('nota.pdf')).toHaveCount(0);
});

test('o seletor de regras salvas é operável pelo teclado', async ({ page }) => {
  await page.goto('/');
  await fillRules(page, 0);
  const trigger = page.getByRole('button', { name: /Regras salvas/ });
  await trigger.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog', { name: m.picker.label })).toBeVisible();
  await page.keyboard.type('juros');
  await expect(page.getByRole('option')).toHaveCount(1);
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Space');
  await expect(page.getByRole('option')).toHaveAttribute('aria-selected', 'true');
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: m.picker.label })).toHaveCount(0);
  await expect(trigger).toBeFocused();
});
