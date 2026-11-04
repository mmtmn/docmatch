#!/usr/bin/env node
// Serves dist/ from a process that stays in the foreground: Playwright's webServer and
// `make preview` use it. It mirrors the CloudFront viewer-request function in docs/headers.md, so
// what the e2e suite exercises is what production serves: clean URLs map to `<path>.html`
// (build.format: 'file'), `/` to index.html, unknown paths to 404.html with status 404.
//
// `astro preview` is deliberately not used: Astro 7 turns it into a background daemon when it
// detects an agent or a non-TTY stdout, so test runs exited early and left stale servers behind.
import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { createServer } from 'node:http';
import { extname, join, posix, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const DEFAULT_HOST = '127.0.0.1';
const DEFAULT_PORT = 4173;

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.map': 'application/json; charset=utf-8',
  '.xml': 'application/xml; charset=utf-8',
  '.txt': 'text/plain; charset=utf-8',
  '.webmanifest': 'application/manifest+json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.avif': 'image/avif',
  '.ico': 'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
  '.wav': 'audio/wav',
  '.mp3': 'audio/mpeg',
  '.m4a': 'audio/mp4',
  '.mp4': 'video/mp4',
  '.webm': 'video/webm',
  '.pdf': 'application/pdf',
};

/** The same rewrite as the CloudFront function in docs/headers.md (keep the two in sync). */
export function rewriteUri(uri) {
  if (uri.endsWith('/')) return uri === '/' ? '/index.html' : `${uri.slice(0, -1)}.html`;
  if (!uri.includes('.')) return `${uri}.html`;
  return uri;
}

/** Cache policy per path family, as docs/headers.md specifies for CloudFront. */
export function cacheControl(filePath) {
  if (filePath.startsWith('/_astro/')) return 'public, max-age=31536000, immutable';
  if (filePath.endsWith('.html')) return 'public, max-age=0, must-revalidate';
  return 'public, max-age=3600';
}

function contentType(filePath) {
  return MIME[extname(filePath).toLowerCase()] ?? 'application/octet-stream';
}

/**
 * Resolves a request URL to a file inside `root`, or null when the URL is malformed or escapes
 * the directory. Query strings are ignored; `..` segments collapse before the root is applied.
 */
export function resolveFile(root, requestUrl) {
  let pathname;
  try {
    pathname = decodeURIComponent(new URL(requestUrl, 'http://localhost').pathname);
  } catch {
    return null;
  }
  if (pathname.includes('\0')) return null;
  const rewritten = rewriteUri(posix.normalize(pathname));
  const file = resolve(root, `.${rewritten}`);
  if (file !== root && !file.startsWith(root + sep)) return null;
  return { file, path: rewritten };
}

async function fileStat(file) {
  try {
    const info = await stat(file);
    return info.isFile() ? info : null;
  } catch {
    return null;
  }
}

function send(response, method, status, file, info, path) {
  response.writeHead(status, {
    'Content-Type': contentType(file),
    'Content-Length': info.size,
    'Cache-Control': cacheControl(path),
    'X-Content-Type-Options': 'nosniff',
    'X-Frame-Options': 'DENY',
    'Content-Security-Policy': "frame-ancestors 'none'",
    'Referrer-Policy': 'strict-origin-when-cross-origin',
  });
  if (method === 'HEAD') {
    response.end();
    return;
  }
  createReadStream(file).pipe(response);
}

/** Creates the HTTP server for `dir` (absolute or relative to cwd) without listening. */
export function createDistServer(dir) {
  const root = resolve(dir);
  return createServer(async (request, response) => {
    const method = request.method ?? 'GET';
    if (method !== 'GET' && method !== 'HEAD') {
      response.writeHead(405, { Allow: 'GET, HEAD', 'Content-Type': 'text/plain; charset=utf-8' });
      response.end('405');
      return;
    }
    const target = resolveFile(root, request.url ?? '/');
    if (target === null) {
      response.writeHead(400, { 'Content-Type': 'text/plain; charset=utf-8' });
      response.end('400');
      return;
    }
    const info = await fileStat(target.file);
    if (info) {
      send(response, method, 200, target.file, info, target.path);
      return;
    }
    const notFound = join(root, '404.html');
    const notFoundInfo = await fileStat(notFound);
    if (notFoundInfo) {
      send(response, method, 404, notFound, notFoundInfo, '/404.html');
      return;
    }
    response.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
    response.end('404');
  });
}

/** Starts serving `dir`; resolves with the server and its URL once it listens. */
export function startDistServer({ dir, host = DEFAULT_HOST, port = DEFAULT_PORT }) {
  const server = createDistServer(dir);
  return new Promise((resolvePromise, reject) => {
    server.once('error', reject);
    server.listen(port, host, () => {
      server.off('error', reject);
      const address = server.address();
      const boundPort = typeof address === 'object' && address ? address.port : port;
      resolvePromise({ server, port: boundPort, url: `http://${host}:${boundPort}` });
    });
  });
}

function parseArgs(argv) {
  const options = {
    dir: join(fileURLToPath(new URL('..', import.meta.url)), 'dist'),
    host: DEFAULT_HOST,
    port: DEFAULT_PORT,
  };
  for (let i = 0; i < argv.length; i += 1) {
    const [flag, inline] = argv[i].split('=', 2);
    const value = inline ?? argv[i + 1];
    if (flag === '--dir') options.dir = value;
    else if (flag === '--host') options.host = value;
    else if (flag === '--port') options.port = Number(value);
    else throw new Error(`opção desconhecida: ${argv[i]}`);
    if (inline === undefined) i += 1;
  }
  if (!Number.isInteger(options.port) || options.port < 0 || options.port > 65535) {
    throw new Error(`porta inválida: ${String(options.port)}`);
  }
  return options;
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const root = resolve(options.dir);
  if (!(await fileStat(join(root, 'index.html')))) {
    console.error(`serve-dist: ${root}/index.html não existe; rode \`pnpm build\` antes.`);
    process.exit(1);
  }
  const { server, url } = await startDistServer({
    dir: root,
    host: options.host,
    port: options.port,
  });
  console.log(`serve-dist: ${root} em ${url}`);
  const shutdown = () => {
    server.close(() => process.exit(0));
    server.closeAllConnections();
    setTimeout(() => process.exit(0), 1000).unref();
  };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  main().catch((error) => {
    console.error(`serve-dist: ${error instanceof Error ? error.message : String(error)}`);
    process.exit(1);
  });
}
