import { describe, expect, it } from 'vitest';
import { transportDirectives } from './csp';

describe('CSP transport policy', () => {
  it.each(['http://localhost:4321', 'http://127.0.0.1:4321', 'http://[::1]:4321'])(
    'allows the explicitly local HTTP server at %s',
    (site) => expect(transportDirectives(site)).toEqual([]),
  );

  it.each([
    'https://localhost:4321',
    'https://docmatch.example',
    'http://docmatch.example',
    'http://localhost.example',
    'http://10.0.0.1',
  ])('preserves HTTPS upgrades for %s', (site) => {
    expect(transportDirectives(site)).toEqual(['upgrade-insecure-requests']);
  });
});
