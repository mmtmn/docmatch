// @vitest-environment node
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { cacheControl, resolveFile, rewriteUri, startDistServer } from './serve-dist.mjs';

describe('rewriteUri (same rules as the CloudFront function in docs/headers.md)', () => {
  it.each([
    ['/', '/index.html'],
    ['/termos', '/termos.html'],
    ['/blog/', '/blog.html'],
    ['/docs/usar/', '/docs/usar/index.html'],
    ['/docs/desenvolver/', '/docs/desenvolver/index.html'],
    ['/docs/desenvolver/python', '/docs/desenvolver/python.html'],
    ['/.well-known/change-password', '/.well-known/change-password.html'],
    ['/blog/primeiro-post', '/blog/primeiro-post.html'],
    ['/.well-known/security.txt', '/.well-known/security.txt'],
    ['/_astro/app.abc123.css', '/_astro/app.abc123.css'],
    ['/sitemap-index.xml', '/sitemap-index.xml'],
  ])('%s → %s', (uri, expected) => {
    expect(rewriteUri(uri)).toBe(expected);
  });
});

describe('cacheControl', () => {
  it('matches the policy per path family', () => {
    expect(cacheControl('/_astro/app.css')).toContain('immutable');
    expect(cacheControl('/termos.html')).toBe('public, max-age=0, must-revalidate');
    expect(cacheControl('/robots.txt')).toBe('public, max-age=3600');
  });
});

describe('resolveFile', () => {
  const root = '/srv/dist';

  it('keeps every path inside the root', () => {
    expect(resolveFile(root, '/../../etc/passwd')?.file).toBe('/srv/dist/etc/passwd.html');
    expect(resolveFile(root, '/%2e%2e/%2e%2e/etc/passwd')?.file).toBe('/srv/dist/etc/passwd.html');
    expect(resolveFile(root, '/termos?utm=1')?.file).toBe('/srv/dist/termos.html');
  });

  it('rejects malformed URLs', () => {
    expect(resolveFile(root, '/%ZZ')).toBeNull();
    expect(resolveFile(root, '/a%00b')).toBeNull();
  });
});

describe('startDistServer', () => {
  let dir;
  let server;
  let url;

  beforeAll(async () => {
    dir = mkdtempSync(join(tmpdir(), 'docmatch-serve-dist-'));
    mkdirSync(join(dir, 'blog'));
    mkdirSync(join(dir, '_astro'));
    mkdirSync(join(dir, '.well-known'));
    mkdirSync(join(dir, 'pasta'));
    writeFileSync(join(dir, 'index.html'), '<title>home</title>');
    writeFileSync(join(dir, 'termos.html'), '<title>termos</title>');
    writeFileSync(join(dir, 'blog.html'), '<title>blog</title>');
    writeFileSync(join(dir, 'blog', 'post.html'), '<title>post</title>');
    writeFileSync(join(dir, '404.html'), '<title>404</title>');
    writeFileSync(join(dir, '_astro', 'app.css'), 'body{}');
    writeFileSync(join(dir, '.well-known', 'security.txt'), 'Contact: x\n');
    const started = await startDistServer({ dir, host: '127.0.0.1', port: 0 });
    server = started.server;
    url = started.url;
  });

  afterAll(async () => {
    await new Promise((resolve) => server.close(resolve));
    rmSync(dir, { recursive: true, force: true });
  });

  it('serves clean URLs as <path>.html', async () => {
    expect(await (await fetch(`${url}/`)).text()).toBe('<title>home</title>');
    expect(await (await fetch(`${url}/termos`)).text()).toBe('<title>termos</title>');
    expect(await (await fetch(`${url}/blog/`)).text()).toBe('<title>blog</title>');
    expect(await (await fetch(`${url}/blog/post`)).text()).toBe('<title>post</title>');
    const html = await fetch(`${url}/termos`);
    expect(html.headers.get('content-type')).toBe('text/html; charset=utf-8');
    expect(html.headers.get('cache-control')).toBe('public, max-age=0, must-revalidate');
    expect(html.headers.get('x-content-type-options')).toBe('nosniff');
  });

  it('keeps relative book assets resolvable when an index URL lacks a slash', async () => {
    for (const guide of ['usar', 'desenvolver']) {
      const result = await fetch(`${url}/docs/${guide}?search=projeto`, { redirect: 'manual' });
      expect(result.status).toBe(301);
      expect(result.headers.get('location')).toBe(`/docs/${guide}/?search=projeto`);
    }
  });

  it('serves assets with their type and the immutable policy for /_astro/', async () => {
    const css = await fetch(`${url}/_astro/app.css`);
    expect(css.status).toBe(200);
    expect(css.headers.get('content-type')).toBe('text/css; charset=utf-8');
    expect(css.headers.get('cache-control')).toContain('immutable');
    const txt = await fetch(`${url}/.well-known/security.txt`);
    expect(txt.headers.get('content-type')).toBe('text/plain; charset=utf-8');
    expect(await txt.text()).toBe('Contact: x\n');
  });

  it('answers unknown paths and directories with 404.html and status 404', async () => {
    const missing = await fetch(`${url}/nao-existe`);
    expect(missing.status).toBe(404);
    expect(await missing.text()).toBe('<title>404</title>');
    expect((await fetch(`${url}/pasta`)).status).toBe(404);
    expect((await fetch(`${url}/pasta/`)).status).toBe(404);
    expect((await fetch(`${url}/_astro/`)).status).toBe(404);
  });

  it('never leaves the directory', async () => {
    expect((await fetch(`${url}/../../../etc/passwd`)).status).toBe(404);
    expect((await fetch(`${url}/%2e%2e/%2e%2e/etc/passwd`)).status).toBe(404);
    expect((await fetch(`${url}/%ZZ`)).status).toBe(400);
  });

  it('supports HEAD and rejects other methods', async () => {
    const head = await fetch(`${url}/termos`, { method: 'HEAD' });
    expect(head.status).toBe(200);
    expect(head.headers.get('content-length')).toBe(String('<title>termos</title>'.length));
    expect(await head.text()).toBe('');
    expect((await fetch(`${url}/termos`, { method: 'POST' })).status).toBe(405);
  });
});
