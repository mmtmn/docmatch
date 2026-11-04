import { describe, expect, it } from 'vitest';
import { checkSources } from './i18n-check.mjs';

const messages = {
  landing: { title: 'Olá' },
  faq: { items: [{ q: 'p', a: 'r' }] },
  nav: { home: 'Início', unused: 'Nunca' },
  tags: ['a', 'b'],
};

describe('i18n-check', () => {
  it('accepts existing keys with the right shape', () => {
    const result = checkSources({
      messages,
      files: [
        { path: 'a.tsx', text: "t('landing.title'); tf('faq.items'); tl(\"tags\")" },
        { path: 'b.astro', text: "const K = { home: 'nav.home' }; t(K.home)" },
      ],
    });
    expect(result.problems).toEqual([]);
    expect(result.unused).toEqual(['nav.unused']);
    expect(result.usedCount).toBe(3);
  });

  it('reports missing keys and wrong shapes with the file name', () => {
    const result = checkSources({
      messages,
      files: [{ path: 'x.tsx', text: "t('nao.existe'); t('faq.items'); tf('landing.title')" }],
    });
    expect(result.problems).toEqual([
      'x.tsx: chave "nao.existe" não existe em pt-BR.json',
      'x.tsx: chave "faq.items" não tem o formato esperado por t()',
      'x.tsx: chave "landing.title" não tem o formato esperado por tf()',
    ]);
  });

  it('ignores dynamic keys', () => {
    const result = checkSources({
      messages,
      files: [{ path: 'x.tsx', text: 't(`plans.${id}_name`)' }],
    });
    expect(result.problems).toEqual([]);
  });

  it('reports reserved keys apart from stale ones, and flags reserved entries in use', () => {
    const result = checkSources({
      messages: { ...messages, auth: { login: 'Entrar', signup: 'Criar conta' } },
      files: [{ path: 'a.tsx', text: "t('landing.title'); t('nav.home'); t('nav.unused')" }],
      reserved: ['auth.', 'tags', 'nav.unused'],
    });
    expect(result.unused).toEqual(['faq.items']);
    expect([...result.reserved].sort()).toEqual(['auth.login', 'auth.signup', 'tags']);
    expect(result.staleReserved).toEqual(['nav.unused']);
  });
});
