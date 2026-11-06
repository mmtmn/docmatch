// CloudFront intentionally delegates script/style policy to Astro's per-page CSP.
// Refuse a publication without that protection, including an externally supplied --dist.
import { createHash } from 'node:crypto';
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';

const directory = process.argv[2];
if (!directory) throw new Error('Usage: check-csp.mjs <dist>');
let count = 0;
for (const name of await readdir(directory, { recursive: true })) {
  if (!name.endsWith('.html')) continue;
  const html = await readFile(path.join(directory, name), 'utf8');
  const meta = html.match(/<meta\b[^>]*http-equiv="content-security-policy"[^>]*>/i)?.[0];
  const policy = meta?.match(/\bcontent="([^"]*)"/i)?.[1] ?? '';
  if (
    !policy.includes("default-src 'self'") ||
    !policy.includes("object-src 'none'") ||
    !policy.includes("script-src 'self'") ||
    !policy.includes("style-src 'self'") ||
    /'unsafe-inline'|'unsafe-eval'/.test(policy)
  )
    throw new Error(`${name}: missing or unsafe page CSP`);
  for (const match of html.matchAll(/<(script|style)\b([^>]*)>([\s\S]*?)<\/\1>/gi)) {
    const [, tag, attributes, content] = match;
    if (/\bsrc\s*=/.test(attributes) || !content.trim()) continue;
    if (tag === 'script' && /type="application\/(ld\+)?json"/i.test(attributes)) continue;
    const hash = createHash('sha256').update(content).digest('base64');
    if (!policy.includes(`'sha256-${hash}'`))
      throw new Error(`${name}: inline ${tag} lacks a CSP hash`);
  }
  count++;
}
if (!count) throw new Error('No HTML pages found');
console.log(`Verified strict per-page CSP on ${count} HTML pages.`);
