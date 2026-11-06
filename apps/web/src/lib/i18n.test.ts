import { describe, expect, it } from 'vitest';
import { hasMessage, interpolate, t, tf, tl, type MessageKey } from './i18n';

describe('i18n', () => {
  it('returns the pt-BR string for a key', () => {
    expect(t('landing.title')).toBe('Seus documentos. Novas possibilidades.');
    expect(t('form.submit')).toBe('Conciliar');
  });

  it('interpolates {vars}, formatting numbers in pt-BR', () => {
    expect(t('form.estimate', { expected: 1200, cap: 1800 })).toBe('≈ 1.200 tokens (máx. 1.800)');
    expect(t('dropzone.remove_file', { name: 'extrato.ofx' })).toBe('Remover extrato.ofx');
  });

  it('keeps unknown placeholders and ignores extra vars', () => {
    expect(interpolate('{a} e {b}', { a: 'x', c: 'y' })).toBe('x e {b}');
  });

  it('renders a visible fallback for a missing key instead of an empty string', () => {
    expect(t('nope.missing' as MessageKey)).toBe('[nope.missing]');
  });

  it('tl returns [] when the key is not a string list', () => {
    expect(tl('how.steps' as never)).toEqual([]);
  });

  it('tf returns structured lists', () => {
    const items = tf('faq.items');
    expect(items.length).toBeGreaterThanOrEqual(3);
    expect(typeof items[0]?.q).toBe('string');
    expect(typeof items[0]?.a).toBe('string');
    expect(tf('legal.terms.sections')[0]?.paragraphs.length).toBeGreaterThan(0);
  });

  it('hasMessage only accepts string leaves', () => {
    expect(hasMessage('landing.title')).toBe(true);
    expect(hasMessage('landing')).toBe(false);
    expect(hasMessage('faq.items')).toBe(false);
  });
});
