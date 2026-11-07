import { expect, test } from '@playwright/test';
import { m } from './messages';

// Emulates the VisualViewport API shrinking when the on-screen keyboard opens.
const mobile = [
  { width: 360, height: 640 },
  { width: 390, height: 844 },
];

interface FakeViewportWindow extends Window {
  __vv: EventTarget & { height: number; offsetTop: number };
}

for (const viewport of mobile) {
  test(`teclado aberto em ${viewport.width}×${viewport.height}: o botão fica acima do teclado`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.addInitScript(() => {
      class FakeVisualViewport extends EventTarget {
        height = window.innerHeight;
        width = window.innerWidth;
        offsetTop = 0;
        offsetLeft = 0;
        pageTop = 0;
        pageLeft = 0;
        scale = 1;
        onresize = null;
        onscroll = null;
      }
      const fake = new FakeVisualViewport();
      Object.defineProperty(window, 'visualViewport', { configurable: true, get: () => fake });
      (window as unknown as FakeViewportWindow).__vv = fake;
    });
    await page.goto('/');

    const textarea = page.getByLabel(m.form.rules_label);
    await expect.poll(() => textarea.evaluate((el) => el.style.height)).toMatch(/px$/);
    await textarea.fill(Array.from({ length: 12 }, (_, i) => `linha ${i + 1}`).join('\n'));
    await textarea.focus();

    const keyboardHeight = Math.round(viewport.height * 0.55);
    await page.evaluate((height) => {
      const fake = (window as unknown as FakeViewportWindow).__vv;
      fake.height = height;
      fake.offsetTop = 0;
      fake.dispatchEvent(new Event('resize'));
    }, keyboardHeight);

    await expect(page.locator('html')).toHaveAttribute('data-vv', 'keyboard');
    const button = page.getByRole('button', { name: m.form.submit });
    await expect(button).toBeVisible();
    const box = (await button.boundingBox())!;
    expect(box.y + box.height).toBeLessThanOrEqual(keyboardHeight + 0.5);

    const area = (await textarea.boundingBox())!;
    expect(area.y).toBeGreaterThanOrEqual(0);
    expect(area.y + area.height).toBeLessThanOrEqual(keyboardHeight + 0.5);
    const lines = await textarea.evaluate((el) => {
      const style = getComputedStyle(el);
      const padding = Number.parseFloat(style.paddingTop) + Number.parseFloat(style.paddingBottom);
      return (el.clientHeight - padding) / Number.parseFloat(style.lineHeight);
    });
    expect(lines).toBeLessThanOrEqual(3.1);

    await page.evaluate((height) => {
      const fake = (window as unknown as FakeViewportWindow).__vv;
      fake.height = height;
      fake.dispatchEvent(new Event('resize'));
    }, viewport.height);
    await expect(page.locator('html')).not.toHaveAttribute('data-vv', /.+/);
  });
}
