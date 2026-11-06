// Tests of the CloudFront viewer-request function (modules/edge/templates/viewer-request.js.tftpl).
// The template is rendered the way modules/edge/main.tf renders it (every ${name} becomes the
// jsonencode() of a value), the `cloudfront` module is replaced by a stub and the handler is run
// against synthetic viewer-request events. Executed by `make -C infra test` (node --test).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import vm from 'node:vm';

const here = dirname(fileURLToPath(import.meta.url));
const templatePath = join(here, '..', 'terraform', 'modules', 'edge', 'templates', 'viewer-request.js.tftpl');

// Representative values; the module passes its variables through jsonencode() exactly like this.
const APEX = 'example.com';
const SECRET_HEADER = 'x-docmatch-origin-secret';
const VIEWER_IP_HEADER = 'x-docmatch-viewer-ip';
const VIEWER_IP = '10.20.30.40';
const templateValues = {
  apex: APEX,
  secret_header: SECRET_HEADER,
  viewer_ip_header: VIEWER_IP_HEADER,
  scanner_regex: 'sqlmap|nikto|nmap|masscan|zgrab|nuclei',
  probe_prefixes: ['/.env', '/.git', '/wp-admin', '/wp-login.php', '/xmlrpc.php', '/cgi-bin', '/phpmyadmin'],
  probe_suffixes: ['.php', '.asp', '.aspx', '.jsp', '.sql', '.bak', '.env'],
  api_prefixes: ['/v1/'],
};

function render(template, values) {
  const missing = new Set();
  const code = template.replace(/\$\{([a-z_]+)\}/g, (_, name) => {
    if (!(name in values)) { missing.add(name); return ''; }
    return JSON.stringify(values[name]);
  });
  assert.equal(missing.size, 0, `template variables without a value: ${[...missing].join(', ')}`);
  assert.doesNotMatch(code, /\$\{/, 'every interpolation must be rendered');
  return code;
}

// Loads the rendered function with a stub of the CloudFront runtime module. kvs.get() resolves
// to the stored value or rejects when the key is absent, like the real key-value store.
function load(kvsValues) {
  const template = readFileSync(templatePath, 'utf8');
  const code = render(template, templateValues);
  const stripped = code.replace(/^import cf from 'cloudfront';\s*$/m, '');
  assert.notEqual(stripped, code, 'the template must import cf from cloudfront');
  const cf = {
    kvs: () => ({
      get: async (key) => {
        if (Object.prototype.hasOwnProperty.call(kvsValues, key)) { return kvsValues[key]; }
        throw new Error(`KeyNotFound: ${key}`);
      },
    }),
  };
  const sandbox = { cf };
  vm.runInNewContext(stripped, sandbox, { filename: 'viewer-request.js' });
  assert.equal(typeof sandbox.handler, 'function', 'the template must declare handler(event)');
  return sandbox.handler;
}

const handler = load({ origin_secret: 's3cret' });

function event({ uri = '/', host = APEX, ua = 'Mozilla/5.0 (X11; Linux) Firefox/128.0', query = {}, headers = {} } = {}) {
  const h = { host: { value: host }, ...headers };
  if (ua !== null) { h['user-agent'] = { value: ua }; }
  return { request: { method: 'GET', uri, querystring: query, headers: h }, viewer: { ip: VIEWER_IP } };
}

async function rewrite(uri) {
  const result = await handler(event({ uri }));
  assert.equal(result.statusCode, undefined, `${uri} must reach the origin`);
  return result.uri;
}

test('directory index: the root becomes /index.html, never //index.html', async () => {
  assert.equal(await rewrite('/'), '/index.html');
});

test('clean URLs: /docs and /docs/ both map to /docs.html', async () => {
  assert.equal(await rewrite('/docs'), '/docs.html');
  assert.equal(await rewrite('/docs/'), '/docs.html');
});

test('books: directory indexes preserve relative assets and query strings', async () => {
  for (const guide of ['usar', 'desenvolver']) {
    const result = await handler(event({ uri: `/docs/${guide}`, query: { search: { value: 'projeto' } } }));
    assert.equal(result.statusCode, 301);
    assert.equal(result.headers.location.value, `/docs/${guide}/?search=projeto`);
    assert.equal(await rewrite(`/docs/${guide}/`), `/docs/${guide}/index.html`);
    assert.equal(await rewrite(`/docs/${guide}/projetos`), `/docs/${guide}/projetos.html`);
    assert.equal(await rewrite(`/docs/${guide}/css/book.123.css`), `/docs/${guide}/css/book.123.css`);
  }
});

test('clean URLs: a page without an extension gets .html, at any depth', async () => {
  assert.equal(await rewrite('/termos'), '/termos.html');
  assert.equal(await rewrite('/blog/codigo-primeiro-modelo-depois'), '/blog/codigo-primeiro-modelo-depois.html');
  assert.equal(await rewrite('/docs/api/autenticacao'), '/docs/api/autenticacao.html');
});

test('clean URLs: a dot in a parent directory does not count as an extension', async () => {
  assert.equal(await rewrite('/.well-known/change-password'), '/.well-known/change-password.html');
});

test('files with an extension are left alone', async () => {
  assert.equal(await rewrite('/app.html'), '/app.html');
  assert.equal(await rewrite('/assets/app.3f2a1c.js'), '/assets/app.3f2a1c.js');
  assert.equal(await rewrite('/.well-known/security.txt'), '/.well-known/security.txt');
});

test('the root keeps its query string (the rewrite only touches the URI)', async () => {
  const result = await handler(event({ uri: '/', query: { utm: { value: 'x' } } }));
  assert.equal(result.uri, '/index.html');
  assert.deepEqual(result.querystring, { utm: { value: 'x' } });
});

test('API paths are never rewritten and carry the origin secret', async () => {
  for (const uri of ['/v1/runs', '/v1/auth/google/callback', '/v1/me']) {
    const result = await handler(event({ uri }));
    assert.equal(result.uri, uri);
    assert.equal(result.headers[SECRET_HEADER].value, 's3cret');
  }
});

test('scanner probe paths get an empty, uncacheable 404 before any origin', async () => {
  for (const uri of ['/wp-login.php', '/.git/config', '/foo/.env', '/backup/db.sql', '/admin/Config.PHP']) {
    const result = await handler(event({ uri }));
    assert.equal(result.statusCode, 404, uri);
    assert.equal(result.headers['cache-control'].value, 'no-store', uri);
    assert.equal(result.body, undefined, uri);
  }
});

test('path traversal attempts are dropped', async () => {
  assert.equal((await handler(event({ uri: '/../etc/passwd' }))).statusCode, 404);
  assert.equal((await handler(event({ uri: '/docs/%2E%2E/x' }))).statusCode, 404);
});

test('scanner user agents are dropped; a missing user agent is only tolerated on the API', async () => {
  assert.equal((await handler(event({ uri: '/', ua: 'sqlmap/1.8' }))).statusCode, 404);
  assert.equal((await handler(event({ uri: '/', ua: '' }))).statusCode, 404);
  assert.equal((await handler(event({ uri: '/', ua: null }))).statusCode, 404);
  assert.equal((await handler(event({ uri: '/v1/webhooks/stripe', ua: null }))).uri, '/v1/webhooks/stripe');
});

test('www is redirected to the apex with the path and query preserved', async () => {
  const result = await handler(event({
    host: 'www.' + APEX,
    uri: '/docs',
    query: { a: { value: '1' }, b: { value: 'x y', multiValue: [{ value: 'x y' }, { value: '2' }] } },
  }));
  assert.equal(result.statusCode, 301);
  assert.equal(result.headers.location.value, `https://${APEX}/docs?a=1&b=x%20y&b=2`);
  const root = await handler(event({ host: 'WWW.' + APEX, uri: '/' }));
  assert.equal(root.statusCode, 301);
  assert.equal(root.headers.location.value, `https://${APEX}/`);
});

test('the viewer address header is overwritten, whatever the viewer sent', async () => {
  const spoofed = { [VIEWER_IP_HEADER]: { value: '10.9.9.9' } };
  const result = await handler(event({ uri: '/v1/me', headers: spoofed }));
  assert.equal(result.headers[VIEWER_IP_HEADER].value, VIEWER_IP);
});

test('a missing origin_secret key does not break the request (the origin answers 403)', async () => {
  const withoutKey = load({});
  const result = await withoutKey(event({ uri: '/' }));
  assert.equal(result.uri, '/index.html');
  assert.equal(result.headers[SECRET_HEADER], undefined);
});
