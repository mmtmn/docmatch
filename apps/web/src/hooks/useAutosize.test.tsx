import { render } from '@testing-library/react';
import { useRef, useState } from 'react';
import { describe, expect, it } from 'vitest';
import { fitTextarea, useAutosize } from './useAutosize';

function prepare(el: HTMLTextAreaElement, scrollHeight: number): void {
  el.style.lineHeight = '24px';
  el.style.paddingTop = '8px';
  el.style.paddingBottom = '8px';
  // jsdom computes border widths as 0 (no layout); the 1px borders are covered by the e2e suite.
  Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => scrollHeight });
}

describe('fitTextarea', () => {
  it('never goes below minLines', () => {
    const el = document.createElement('textarea');
    prepare(el, 10);
    const result = fitTextarea(el, 1, 6);
    expect(result).toEqual({ height: 40, overflowing: false });
    expect(el.style.height).toBe('40px');
    expect(el.style.overflowY).toBe('hidden');
  });

  it('grows with the content up to maxLines', () => {
    const el = document.createElement('textarea');
    prepare(el, 100);
    expect(fitTextarea(el, 1, 6)).toEqual({ height: 100, overflowing: false });
  });

  it('caps at maxLines and scrolls internally beyond it', () => {
    const el = document.createElement('textarea');
    prepare(el, 40 * 24 + 16);
    const result = fitTextarea(el, 1, 6);
    expect(result).toEqual({ height: 24 * 6 + 16, overflowing: true });
    expect(el.style.overflowY).toBe('auto');
  });

  it('falls back to sane metrics when styles are missing', () => {
    const el = document.createElement('textarea');
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => 0 });
    const result = fitTextarea(el, 1, 6);
    expect(result.height).toBeGreaterThan(0);
  });
});

function Harness({ initial }: { initial: string }) {
  const ref = useRef<HTMLTextAreaElement>(null);
  const [value, setValue] = useState(initial);
  useAutosize(ref, value, { minLines: 1, maxLines: 6 });
  return (
    <textarea
      ref={ref}
      value={value}
      aria-label="regras"
      onChange={(event) => setValue(event.target.value)}
      style={{ lineHeight: '20px', paddingTop: '0px', paddingBottom: '0px' }}
    />
  );
}

describe('useAutosize', () => {
  it('applies a height through the CSSOM after render', () => {
    const { getByLabelText } = render(<Harness initial="" />);
    const el = getByLabelText('regras') as HTMLTextAreaElement;
    expect(el.style.height).toMatch(/px$/);
    expect(Number.parseFloat(el.style.height)).toBeGreaterThanOrEqual(20);
  });
});
