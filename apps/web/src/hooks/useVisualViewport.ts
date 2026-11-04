import { useEffect, useState } from 'react';

export interface ViewportState {
  /** Visible height in px (visual viewport when available, else the layout viewport). */
  height: number;
  /** Offset of the visual viewport from the top of the layout viewport. */
  offsetTop: number;
  /** True when the visible area shrank enough to be an on-screen keyboard. */
  keyboardOpen: boolean;
}

/** A keyboard takes at least this much of the layout viewport; pinch-zoom is excluded by scale. */
export const KEYBOARD_MIN_PX = 120;

const CLOSED: ViewportState = { height: 0, offsetTop: 0, keyboardOpen: false };

export function readViewport(win: Window = window): ViewportState {
  const layoutHeight = win.innerHeight;
  const vv = win.visualViewport;
  if (!vv) return { height: layoutHeight, offsetTop: 0, keyboardOpen: false };
  const height = Math.round(vv.height);
  const zoomed = typeof vv.scale === 'number' && vv.scale > 1.01;
  const keyboardOpen = !zoomed && layoutHeight - height >= KEYBOARD_MIN_PX;
  return { height, offsetTop: Math.max(0, Math.round(vv.offsetTop)), keyboardOpen };
}

/**
 * Pins the first screen to the visual viewport while the keyboard is open. Uses CSS custom
 * properties set through the CSSOM plus a guarding attribute (see global.css), so browsers without
 * the VisualViewport API keep the 100dvh fallback untouched.
 */
export function applyViewport(
  state: ViewportState,
  root: HTMLElement = document.documentElement,
): void {
  if (state.keyboardOpen) {
    root.style.setProperty('--vv-height', `${state.height}px`);
    root.style.setProperty('--vv-top', `${state.offsetTop}px`);
    root.setAttribute('data-vv', 'keyboard');
  } else {
    root.style.removeProperty('--vv-height');
    root.style.removeProperty('--vv-top');
    root.removeAttribute('data-vv');
  }
}

export function useVisualViewport(enabled = true): ViewportState {
  const [state, setState] = useState<ViewportState>(CLOSED);

  useEffect(() => {
    if (!enabled || typeof window === 'undefined') return undefined;
    const vv = window.visualViewport;
    const update = (): void => {
      const next = readViewport();
      applyViewport(next);
      setState((prev) =>
        prev.height === next.height &&
        prev.offsetTop === next.offsetTop &&
        prev.keyboardOpen === next.keyboardOpen
          ? prev
          : next,
      );
    };
    update();
    vv?.addEventListener('resize', update);
    vv?.addEventListener('scroll', update);
    window.addEventListener('resize', update);
    return () => {
      vv?.removeEventListener('resize', update);
      vv?.removeEventListener('scroll', update);
      window.removeEventListener('resize', update);
      applyViewport(CLOSED);
    };
  }, [enabled]);

  return state;
}
