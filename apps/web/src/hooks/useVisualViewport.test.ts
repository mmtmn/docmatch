import { afterEach, describe, expect, it } from 'vitest';
import { applyViewport, KEYBOARD_MIN_PX, readViewport } from './useVisualViewport';

class FakeVisualViewport extends EventTarget {
  height = 800;
  width = 400;
  offsetTop = 0;
  scale = 1;
}

afterEach(() => {
  applyViewport({ height: 0, offsetTop: 0, keyboardOpen: false });
});

describe('readViewport', () => {
  it('reports no keyboard without the VisualViewport API', () => {
    const win = { innerHeight: 700, visualViewport: null } as unknown as Window;
    expect(readViewport(win)).toEqual({ height: 700, offsetTop: 0, keyboardOpen: false });
  });

  it('detects the keyboard when the visual viewport shrinks past the threshold', () => {
    const vv = new FakeVisualViewport();
    const win = { innerHeight: 800, visualViewport: vv } as unknown as Window;
    vv.height = 800 - KEYBOARD_MIN_PX + 1;
    expect(readViewport(win).keyboardOpen).toBe(false);
    vv.height = 400;
    vv.offsetTop = 12.4;
    expect(readViewport(win)).toEqual({ height: 400, offsetTop: 12, keyboardOpen: true });
  });

  it('ignores pinch zoom', () => {
    const vv = new FakeVisualViewport();
    vv.height = 300;
    vv.scale = 2;
    const win = { innerHeight: 800, visualViewport: vv } as unknown as Window;
    expect(readViewport(win).keyboardOpen).toBe(false);
  });
});

describe('applyViewport', () => {
  it('sets and clears the CSS variables and the guarding attribute', () => {
    const root = document.documentElement;
    applyViewport({ height: 420, offsetTop: 8, keyboardOpen: true }, root);
    expect(root.getAttribute('data-vv')).toBe('keyboard');
    expect(root.style.getPropertyValue('--vv-height')).toBe('420px');
    expect(root.style.getPropertyValue('--vv-top')).toBe('8px');
    applyViewport({ height: 800, offsetTop: 0, keyboardOpen: false }, root);
    expect(root.hasAttribute('data-vv')).toBe(false);
    expect(root.style.getPropertyValue('--vv-height')).toBe('');
  });
});
