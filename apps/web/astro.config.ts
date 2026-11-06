import react from '@astrojs/react';
import sitemap from '@astrojs/sitemap';
import { defineConfig, envField } from 'astro/config';
import { limitsPlugin } from './vite/limits';
import { pricingPlugin } from './vite/pricing';

// Everything environment-specific comes from variables (see .env.example): the site origin, the
// API origin (CSP connect-src / form-action) and the media origin (CSP media-src).
const site = process.env['DM_PUBLIC_BASE_URL']?.trim() || 'http://localhost:4321';
const apiOrigin = originOf(process.env['PUBLIC_DM_API_BASE_URL']);
const mediaOrigin = originOf(process.env['PUBLIC_DM_MEDIA_BASE_URL']);
// Object stores the browser uploads to with presigned requests (CSP connect-src), space-separated.
const uploadOrigins = (process.env['DM_UPLOAD_ORIGINS'] ?? '')
  .split(/\s+/)
  .filter(Boolean)
  .map((value) => originOf(value));

function originOf(value: string | undefined): string | undefined {
  if (!value) return undefined;
  try {
    return new URL(value).origin;
  } catch {
    throw new Error(`URL inválida na configuração: ${value}`);
  }
}

function sources(...items: Array<string | undefined>): string {
  return items.filter((item): item is string => Boolean(item)).join(' ');
}

export default defineConfig({
  site,
  output: 'static',
  trailingSlash: 'never',
  build: {
    // One HTML file per route (/termos -> termos.html); CloudFront maps clean URLs to it.
    format: 'file',
    // External stylesheets only: with CSP hashes a single inlined <style> would still be allowed,
    // but external files cache better and keep the policy small.
    inlineStylesheets: 'never',
  },
  integrations: [
    react(),
    sitemap({
      // Results, sign-in and account pages are per user and must not be indexed.
      filter: (page) => !/\/(resultados|login|conta)(\/|\.html|$)/.test(page),
    }),
  ],
  security: {
    checkOrigin: true,
    // Astro emits a <meta http-equiv="content-security-policy"> with SHA-256 hashes for the few
    // inline scripts it generates for islands. Directives that only work in HTTP headers
    // (frame-ancestors, report-to) live in the CloudFront policy documented in docs/headers.md.
    csp: {
      algorithm: 'SHA-256',
      directives: [
        "default-src 'self'",
        "base-uri 'self'",
        "object-src 'none'",
        "frame-src 'self'",
        "img-src 'self' data: blob:",
        "font-src 'self'",
        "manifest-src 'self'",
        "worker-src 'self' blob:",
        `connect-src ${sources("'self'", apiOrigin, ...uploadOrigins)}`,
        `media-src ${sources("'self'", 'blob:', mediaOrigin)}`,
        `form-action ${sources("'self'", apiOrigin)}`,
        'upgrade-insecure-requests',
      ],
      scriptDirective: { resources: ["'self'"] },
      styleDirective: { resources: ["'self'"] },
    },
  },
  markdown: {
    // Shiki injects inline styles, which the CSP forbids; blog posts use plain code blocks.
    syntaxHighlight: false,
  },
  env: {
    schema: {
      PUBLIC_DM_API_BASE_URL: envField.string({
        context: 'client',
        access: 'public',
        optional: true,
      }),
      PUBLIC_DM_MEDIA_BASE_URL: envField.string({
        context: 'client',
        access: 'public',
        optional: true,
      }),
      PUBLIC_DM_FEATURE_RESULTS: envField.boolean({
        context: 'client',
        access: 'public',
        default: false,
      }),
      PUBLIC_DM_SECURITY_CONTACT: envField.string({
        context: 'server',
        access: 'public',
        optional: true,
      }),
      PUBLIC_DM_SECURITY_POLICY_URL: envField.string({
        context: 'server',
        access: 'public',
        optional: true,
      }),
      PUBLIC_DM_SOURCE_URL: envField.string({
        context: 'server',
        access: 'public',
        optional: true,
      }),
      PUBLIC_DM_STATUS_URL: envField.string({
        context: 'server',
        access: 'public',
        optional: true,
      }),
    },
  },
  vite: {
    plugins: [limitsPlugin(), pricingPlugin()],
  },
});
