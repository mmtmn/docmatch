import { describe, expect, it } from 'vitest';
import { callbackCode, pkceChallenge, trustedPage, validAPIRequest as validate } from './policy';
const validAPIRequest = (request: Record<string, unknown>) =>
  validate({ sessionEpoch: '00000000-0000-4000-8000-000000000001', ...request });
describe('privileged extension messages', () => {
  it('accepts only the extension UI, excluding content scripts and foreign callers', () => {
    const base = 'chrome-extension://example/';
    const url = (p: string) => base + p;
    expect(
      trustedPage('example', { id: 'example', url: base + 'workspace.html?project=one' }, url),
    ).toBe(true);
    for (const sender of [
      { id: 'other', url: base + 'workspace.html' },
      { id: 'example', url: 'https://evil.example.invalid/workspace.html' },
      { id: 'example', url: base + 'other.html' },
      { id: 'example', url: 'invalid' },
      { id: 'example' },
    ])
      expect(trustedPage('example', sender, url)).toBe(false);
  });
  it('cannot turn the API bridge into an arbitrary network or administration proxy', () => {
    for (const path of [
      'https://evil.example.invalid/v1/projects',
      '//evil.test/v1/projects',
      '/v1/projects/../billing',
      '/v1/projects/%2e%2e/billing',
      '/v1/projects\\../billing',
      '/v1/team',
      '/v1/me/password',
      '/v1/api-keys',
      '/v1/auth/refresh',
    ])
      expect(validAPIRequest({ path })).toBe(false);
    expect(
      validAPIRequest({
        path: '/v1/projects?archived=false',
        orgId: 'org_00000000000000000000000000',
      }),
    ).toBe(true);
    expect(
      validAPIRequest({ path: '/v1/projects', method: 'POST', body: { name: 'Fechamento' } }),
    ).toBe(true);
    expect(validAPIRequest({ path: '/v1/projects', orgId: 'foreign' })).toBe(false);
    expect(validAPIRequest({ path: '/v1/projects', idempotencyKey: 'header\r\ninjection' })).toBe(
      false,
    );
    expect(validAPIRequest({ path: '/v1/projects', body: { text: 'x'.repeat(300000) } })).toBe(
      false,
    );
  });
  it('binds the code to the exact destination and request state', async () => {
    const state = 'a'.repeat(43);
    const code = 'b'.repeat(43);
    const base = 'https://extension.example.invalid/callback';
    expect(callbackCode(`${base}?state=${state}&code=${code}`, base, state)).toBe(code);
    for (const target of [
      `https://evil.example.invalid/callback?state=${state}&code=${code}`,
      `${base}?state=wrong&code=${code}`,
      `${base}?state=${state}&code=${code}&code=other`,
      `${base}?state=${state}&error=access_denied`,
      `${base}?state=${state}&code=${code}#fragment`,
    ])
      expect(() => callbackCode(target, base, state)).toThrow();
    expect(await pkceChallenge('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')).toBe(
      'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
    );
  });
});
