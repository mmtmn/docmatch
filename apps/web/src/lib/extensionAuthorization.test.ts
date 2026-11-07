import { describe, expect, it, vi } from 'vitest';
const mocks = vi.hoisted(() => ({ api: vi.fn() }));
vi.mock('./api', () => ({ api: mocks.api }));
import { authorizeExtension, readExtensionAuthorization } from './extensionAuthorization';
const valid = {
  client_id: 'chrome-dev',
  redirect_uri: 'https://extension.example.invalid/',
  code_challenge: 'a'.repeat(43),
  code_challenge_method: 'S256' as const,
  state: 'b'.repeat(43),
};
describe('extension authorization parameters', () => {
  it('accepts only complete PKCE requests and safe return schemes', () => {
    expect(readExtensionAuthorization(new URLSearchParams(valid).toString())).toEqual(valid);
    for (const change of [
      { state: '' },
      { code_challenge_method: 'plain' },
      { redirect_uri: 'javascript:alert(1)' },
      { redirect_uri: 'unregistered-app://auth' },
      { redirect_uri: 'https://other.example.invalid/#fragment' },
      { redirect_uri: 'https://a:pass@other.example.invalid/' },
      { redirect_uri: 'invalid' },
      { client_id: '../team' },
    ]) {
      expect(
        readExtensionAuthorization(new URLSearchParams({ ...valid, ...change }).toString()),
      ).toBeNull();
    }
  });
  it('accepts the production Safari callback and keeps its destination bound to the request', async () => {
    const request = {
      ...valid,
      client_id: 'safari',
      redirect_uri: 'br.com.docmatch.extension://auth',
    };
    expect(readExtensionAuthorization(new URLSearchParams(request).toString())).toEqual(request);
    const callback = `${request.redirect_uri}?code=${'c'.repeat(43)}&state=${request.state}`;
    mocks.api.mockResolvedValue({ redirect_uri: callback });
    expect(await authorizeExtension(request)).toBe(callback);
    for (const target of [
      callback.replace('br.com.docmatch.extension:', 'docmatch-extension-dev:'),
      callback.replace('://auth?', '://other?'),
    ]) {
      mocks.api.mockResolvedValue({ redirect_uri: target });
      await expect(authorizeExtension(request)).rejects.toThrow();
    }
  });
  it('checks the callback destination and state before navigating', async () => {
    const code = 'c'.repeat(43);
    mocks.api.mockResolvedValue({
      redirect_uri: `${valid.redirect_uri}?code=${code}&state=${valid.state}`,
    });
    expect(await authorizeExtension(valid)).toContain(`code=${code}`);
    for (const target of [
      `https://other.example.invalid/?code=${code}&state=${valid.state}`,
      `${valid.redirect_uri}?code=${code}&state=other`,
      `${valid.redirect_uri}?code=bad&state=${valid.state}`,
    ]) {
      mocks.api.mockResolvedValue({ redirect_uri: target });
      await expect(authorizeExtension(valid)).rejects.toThrow();
    }
  });
});
