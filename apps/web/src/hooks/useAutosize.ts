import { useLayoutEffect, type RefObject } from 'react';

export interface AutosizeOptions {
  minLines?: number;
  maxLines?: number;
  maxHeight?: number;
}

export interface AutosizeResult {
  /** Height applied, in px. */
  height: number;
  /** True when the content needs more than `maxLines`: the textarea scrolls internally. */
  overflowing: boolean;
}

function px(value: string): number {
  const n = Number.parseFloat(value);
  return Number.isFinite(n) ? n : 0;
}

/**
 * Sizes a textarea between `minLines` and `maxLines` of its own line-height. Heights are applied
 * through the CSSOM (`el.style`), which a strict CSP allows, unlike inline `style` attributes in
 * markup. Returns the metrics so callers can expose "overflowing" state.
 */
export function fitTextarea(
  el: HTMLTextAreaElement,
  minLines = 1,
  maxLines = 6,
  maxHeight = Number.POSITIVE_INFINITY,
): AutosizeResult {
  const style = getComputedStyle(el);
  const fontSize = px(style.fontSize) || 16;
  const lineHeight = px(style.lineHeight) || fontSize * 1.5;
  const padding = px(style.paddingTop) + px(style.paddingBottom);
  const border = px(style.borderTopWidth) + px(style.borderBottomWidth);
  const min = lineHeight * Math.max(1, minLines) + padding + border;
  const lineMax = Math.max(
    min,
    Math.min(maxHeight, lineHeight * Math.max(minLines, maxLines) + padding + border),
  );
  const cssMax = px(style.maxHeight);
  const max = cssMax > 0 ? Math.max(min, Math.min(lineMax, cssMax)) : lineMax;

  // Collapse first so scrollHeight reflects the content, not the previous height.
  el.style.height = 'auto';
  const content = el.scrollHeight + border;
  const height = Math.ceil(Math.min(Math.max(content, min), max));
  el.style.height = `${height}px`;
  const measured = el.getBoundingClientRect().height || height;
  const overflowing = content > Math.min(max, measured) + 0.5;
  el.style.overflowY = overflowing ? 'auto' : 'hidden';
  return { height, overflowing };
}

/**
 * React hook: keeps a textarea fitted to its content (1..6 lines by default) and re-fits when the
 * value, the viewport or the element's box changes (the type scale uses clamp(), so a resize can
 * change the line height).
 */
export function useAutosize(
  ref: RefObject<HTMLTextAreaElement | null>,
  value: string,
  options: AutosizeOptions = {},
): void {
  const { minLines = 1, maxLines = 6, maxHeight = Number.POSITIVE_INFINITY } = options;
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return undefined;
    let frame = 0;
    const fit = (): void => {
      fitTextarea(el, minLines, maxLines, maxHeight);
    };
    fit();
    const scheduleFit = (): void => {
      if (frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        fit();
      });
    };
    // Height changes are the result of fitting, not a reason to fit again.
    let width = el.parentElement?.getBoundingClientRect().width;
    const observer =
      typeof ResizeObserver !== 'undefined'
        ? new ResizeObserver((entries) => {
            const next = entries[0]?.contentRect.width;
            if (next !== width) {
              width = next;
              scheduleFit();
            }
          })
        : null;
    observer?.observe(el.parentElement ?? el);
    window.addEventListener('resize', scheduleFit);
    return () => {
      if (frame) cancelAnimationFrame(frame);
      observer?.disconnect();
      window.removeEventListener('resize', scheduleFit);
    };
  }, [ref, value, minLines, maxLines, maxHeight]);
}
