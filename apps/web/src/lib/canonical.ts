// With `build.format: 'file'` (astro.config.ts), `Astro.url.pathname` carries the name of the
// output file at build time: `/termos.html`, `/blog/<id>.html`, `/index.html`. The sitemap, the
// navigation links and the CloudFront rewrite (docs/headers.md) all use the clean URL instead, and
// so must `<link rel="canonical">` and `og:url`, or every page declares a different URL as its own.
const HTML_SUFFIX = '.html';
const INDEX_SEGMENT = '/index';

/**
 * Clean URL path for a page: strips a trailing `.html`, maps `/index` to `/` (and `/x/index` to
 * `/x`) and never ends with a slash except at the root (`trailingSlash: 'never'`). A query string
 * or fragment passes through untouched. Idempotent, so dev-server pathnames are left as they are.
 */
export function canonicalPath(pathname: string): string {
  const cut = pathname.search(/[?#]/);
  const path = cut === -1 ? pathname : pathname.slice(0, cut);
  const rest = cut === -1 ? '' : pathname.slice(cut);

  let clean = path.endsWith(HTML_SUFFIX) ? path.slice(0, -HTML_SUFFIX.length) : path;
  if (clean.endsWith(INDEX_SEGMENT)) clean = clean.slice(0, -INDEX_SEGMENT.length);
  clean = clean.replace(/\/+$/, '');
  if (!clean.startsWith('/')) clean = `/${clean}`;
  return `${clean}${rest}`;
}
