import { describe, expect, it } from 'vitest';
import {
  contactUri,
  renderSecurityTxt,
  SECURITY_TXT_TTL_DAYS,
} from '../pages/.well-known/security.txt';

describe('security.txt', () => {
  it('renders the RFC 9116 fields from the environment', () => {
    const now = new Date('2026-11-04T12:00:00Z');
    const text = renderSecurityTxt({
      contact: 'security@example.com',
      policyUrl: 'https://example.com/seguranca',
      canonical: 'https://example.com/.well-known/security.txt',
      now,
    });
    const lines = text.trimEnd().split('\n');
    expect(lines[0]).toBe('Contact: mailto:security@example.com');
    const expires = new Date(lines[1]!.replace('Expires: ', ''));
    expect((expires.getTime() - now.getTime()) / 86_400_000).toBe(SECURITY_TXT_TTL_DAYS);
    expect(SECURITY_TXT_TTL_DAYS).toBeLessThan(365);
    expect(lines).toContain('Preferred-Languages: pt-BR, en');
    expect(lines).toContain('Canonical: https://example.com/.well-known/security.txt');
    expect(lines).toContain('Policy: https://example.com/seguranca');
  });

  it('leaves a visible comment when the contact is not configured', () => {
    const text = renderSecurityTxt({
      contact: undefined,
      policyUrl: undefined,
      canonical: 'https://example.com/.well-known/security.txt',
      now: new Date(),
    });
    expect(text.startsWith('# Contact:')).toBe(true);
    expect(text).not.toContain('Policy:');
  });

  it('normalises contact values', () => {
    expect(contactUri('a@b.co')).toBe('mailto:a@b.co');
    expect(contactUri('mailto:a@b.co')).toBe('mailto:a@b.co');
    expect(contactUri('https://example.com/report')).toBe('https://example.com/report');
  });
});
