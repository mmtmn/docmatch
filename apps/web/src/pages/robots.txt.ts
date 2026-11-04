import type { APIRoute } from 'astro';

// Generated at build time so the sitemap URL follows the configured site origin.
export const GET: APIRoute = ({ site }) => {
  const sitemap = new URL('/sitemap-index.xml', site).href;
  const body = [
    'User-agent: *',
    'Allow: /',
    'Disallow: /resultados',
    '',
    `Sitemap: ${sitemap}`,
    '',
  ].join('\n');
  return new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};
