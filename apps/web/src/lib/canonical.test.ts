import { describe, expect, it } from 'vitest';
import { canonicalPath } from './canonical';

describe('canonicalPath', () => {
  it('strips the .html suffix that build.format "file" puts in the pathname', () => {
    expect(canonicalPath('/termos.html')).toBe('/termos');
    expect(canonicalPath('/blog/x.html')).toBe('/blog/x');
  });

  it('maps the index page to the root', () => {
    expect(canonicalPath('/index.html')).toBe('/');
    expect(canonicalPath('/index')).toBe('/');
    expect(canonicalPath('/')).toBe('/');
    expect(canonicalPath('')).toBe('/');
    expect(canonicalPath('/blog/index.html')).toBe('/blog');
  });

  it('keeps trailingSlash "never" semantics and is idempotent', () => {
    expect(canonicalPath('/termos/')).toBe('/termos');
    expect(canonicalPath('/termos')).toBe('/termos');
    expect(canonicalPath('/indexado.html')).toBe('/indexado');
    expect(canonicalPath(canonicalPath('/blog/x.html'))).toBe('/blog/x');
  });

  it('leaves query string and fragment alone', () => {
    expect(canonicalPath('/termos.html?utm=1#secao')).toBe('/termos?utm=1#secao');
    expect(canonicalPath('/index.html#topo')).toBe('/#topo');
    expect(canonicalPath('/blog/x?pagina=2')).toBe('/blog/x?pagina=2');
  });

  it('composes with the site origin into the URL the sitemap lists', () => {
    const site = 'https://example.com';
    expect(new URL(canonicalPath('/termos.html'), site).href).toBe('https://example.com/termos');
    expect(new URL(canonicalPath('/blog/x.html'), site).href).toBe('https://example.com/blog/x');
    expect(new URL(canonicalPath('/index.html'), site).href).toBe('https://example.com/');
  });
});
