import { PUBLIC_DM_SECURITY_CONTACT, PUBLIC_DM_SECURITY_POLICY_URL } from 'astro:env/server';
import type { APIRoute } from 'astro';

/** Expiry just under a year, as RFC 9116 recommends. */
export const SECURITY_TXT_TTL_DAYS = 364;

export function contactUri(value: string): string {
  const trimmed = value.trim();
  if (/^(mailto:|https?:\/\/|tel:)/i.test(trimmed)) return trimmed;
  return trimmed.includes('@') ? `mailto:${trimmed}` : trimmed;
}

export function renderSecurityTxt(options: {
  contact: string | undefined;
  policyUrl: string | undefined;
  canonical: string;
  now: Date;
}): string {
  const lines: string[] = [];
  if (options.contact) {
    lines.push(`Contact: ${contactUri(options.contact)}`);
  } else {
    lines.push('# Contact: defina PUBLIC_DM_SECURITY_CONTACT no ambiente de build');
  }
  const expires = new Date(options.now.getTime() + SECURITY_TXT_TTL_DAYS * 24 * 60 * 60 * 1000);
  lines.push(`Expires: ${expires.toISOString()}`);
  lines.push('Preferred-Languages: pt-BR, en');
  lines.push(`Canonical: ${options.canonical}`);
  if (options.policyUrl) lines.push(`Policy: ${options.policyUrl}`);
  return `${lines.join('\n')}\n`;
}

export const GET: APIRoute = ({ site }) => {
  if (!PUBLIC_DM_SECURITY_CONTACT) {
    console.warn(
      'security.txt: PUBLIC_DM_SECURITY_CONTACT não definido; o arquivo sai sem Contact.',
    );
  }
  const body = renderSecurityTxt({
    contact: PUBLIC_DM_SECURITY_CONTACT,
    policyUrl: PUBLIC_DM_SECURITY_POLICY_URL,
    canonical: new URL('/.well-known/security.txt', site).href,
    now: new Date(),
  });
  return new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};
