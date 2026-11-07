import { build } from 'vite';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { isIP } from 'node:net';
import { createHash } from 'node:crypto';
import YAML from 'yaml';
import { validateLimits } from '../../web/vite/limits-schema.ts';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const local = process.argv.includes('--local');
const configPath = local
  ? path.join(root, 'local.config.json')
  : process.env.DM_EXTENSION_CONFIG_FILE;
if (!configPath)
  throw new Error('Defina DM_EXTENSION_CONFIG_FILE com as origens e IDs de produção.');
const settings = JSON.parse(await fs.readFile(configPath, 'utf8'));
const publicHost = (hostname) =>
  !isIP(hostname.replace(/^\[|\]$/g, '')) &&
  hostname.includes('.') &&
  !/(^|\.)(localhost|local)$/.test(hostname);
for (const field of ['apiURL', 'siteURL']) {
  const url = new URL(settings[field]);
  if (
    local
      ? !['localhost', '127.0.0.1'].includes(url.hostname)
      : url.protocol !== 'https:' || !publicHost(url.hostname)
  )
    throw new Error(`Origem inválida: ${field}`);
  if (url.pathname !== '/' || url.search || url.hash || url.username || url.password)
    throw new Error(`Use apenas a origem em ${field}`);
}
const hostPattern = (value) => {
  const url = new URL(value);
  if (!local && (url.protocol !== 'https:' || !publicHost(url.hostname)))
    throw new Error('Uploads precisam de uma origem HTTPS pública.');
  if (
    url.hostname.includes('*') ||
    url.username ||
    url.password ||
    url.pathname !== '/' ||
    url.search ||
    url.hash
  )
    throw new Error('Use uma origem exata para uploads.');
  return `${url.protocol}//${url.hostname}/*`;
};
const hostPermissions = [
  ...new Set([settings.apiURL, ...(settings.uploadOrigins ?? [])].map(hostPattern)),
];
const limitsPath = path.join(root, '../../packages/contracts/limits.yaml');
const limits = validateLimits(YAML.parse(await fs.readFile(limitsPath, 'utf8')), limitsPath);
const websiteAPI = path.resolve(root, '../web/src/lib/api');
const sharedViews = {
  name: 'docmatch-extension-shared-views',
  enforce: 'pre',
  resolveId(id, importer) {
    if (id === 'virtual:limits' || id === 'astro:env/client') return '\0' + id;
    if (
      importer &&
      id.startsWith('.') &&
      path.resolve(path.dirname(importer), id).replace(/\.ts$/, '') === websiteAPI
    )
      return path.join(root, 'src/websiteTransport.ts');
    return null;
  },
  load(id) {
    if (id === '\0virtual:limits') return `export default ${JSON.stringify(limits)}`;
    if (id === '\0astro:env/client')
      return `export const PUBLIC_DM_API_BASE_URL = undefined; export const PUBLIC_DM_MEDIA_BASE_URL = undefined; export const PUBLIC_DM_FEATURE_RESULTS = true;`;
    return null;
  },
};
for (const browser of ['chrome', 'firefox', 'safari']) {
  const browserConfig = settings[browser];
  if (!/^[a-z][a-z0-9_-]{2,63}$/.test(browserConfig?.clientID ?? ''))
    throw new Error(`clientID inválido de ${browser}`);
  if (browser !== 'safari') {
    const redirect = new URL(browserConfig.redirectURI);
    if (
      redirect.protocol !== 'https:' ||
      redirect.username ||
      redirect.password ||
      redirect.search ||
      redirect.hash
    )
      throw new Error(`Retorno de ${browser} inválido.`);
  }
  if (browser === 'chrome') {
    if (
      !/^[a-p]{32}$/.test(browserConfig.id ?? '') ||
      browserConfig.redirectURI !== `https://${browserConfig.id}.chromiumapp.org/callback`
    )
      throw new Error('ID ou retorno do Chrome inválido.');
    if (browserConfig.key) {
      const id = [
        ...createHash('sha256')
          .update(Buffer.from(browserConfig.key, 'base64'))
          .digest('hex')
          .slice(0, 32),
      ]
        .map((digit) => String.fromCharCode(97 + Number.parseInt(digit, 16)))
        .join('');
      if (id !== browserConfig.id)
        throw new Error('A chave pública do Chrome não corresponde ao ID registrado.');
    }
  }
  if (browser === 'safari') {
    const expected = local ? 'docmatch-extension-dev://auth' : 'br.com.docmatch.extension://auth';
    if (browserConfig.redirectURI !== expected)
      throw new Error('Retorno nativo do Safari inválido.');
  }
  const outDir = path.join(root, 'dist', local ? 'local' : 'production', browser);
  const define = {
    __EXTENSION_CONFIG__: JSON.stringify({
      apiURL: settings.apiURL,
      siteURL: settings.siteURL,
      clientID: browserConfig.clientID,
      browser,
      local,
      uploadOrigins: (settings.uploadOrigins ?? [settings.apiURL]).map(
        (value) => new URL(value).origin,
      ),
      hostPermissions,
      redirectURI: browserConfig.redirectURI,
    }),
  };
  await build({
    configFile: false,
    root,
    base: './',
    define,
    plugins: [sharedViews],
    build: {
      outDir,
      emptyOutDir: true,
      target: 'es2022',
      rolldownOptions: {
        input: {
          popup: path.join(root, 'popup.html'),
          workspace: path.join(root, 'workspace.html'),
        },
      },
    },
  });
  await build({
    configFile: false,
    root,
    define,
    build: {
      outDir,
      emptyOutDir: false,
      target: 'es2022',
      lib: {
        entry: path.join(root, 'src/background.ts'),
        name: 'docmatchBackground',
        formats: ['iife'],
        fileName: () => 'background.js',
      },
    },
  });
  const manifest = {
    manifest_version: 3,
    name: local ? 'docmatch (local)' : 'docmatch',
    version: '0.2.0',
    description: 'Envie documentos aos seus projetos, concilie e consulte resultados.',
    permissions: [browser === 'safari' ? 'nativeMessaging' : 'identity', 'storage'],
    host_permissions: hostPermissions,
    action: { default_popup: 'popup.html', default_title: 'docmatch' },
    icons: { 128: 'icon.png' },
    background:
      browser === 'chrome' ? { service_worker: 'background.js' } : { scripts: ['background.js'] },
    content_security_policy: {
      extension_pages: "script-src 'self'; object-src 'none'; base-uri 'none'",
    },
  };
  if (browser === 'chrome' && browserConfig.key) manifest.key = browserConfig.key;
  if (browser === 'firefox') {
    if (!browserConfig.id) throw new Error('Falta o ID do Firefox.');
    manifest.browser_specific_settings = {
      gecko: {
        id: browserConfig.id,
        strict_min_version: '140.0',
        data_collection_permissions: {
          required: ['authenticationInfo', 'personallyIdentifyingInfo', 'financialAndPaymentInfo'],
        },
      },
    };
  }
  await fs.copyFile(
    path.join(root, '../web/public/brand/docmatch-avatar-512.png'),
    path.join(outDir, 'icon.png'),
  );
  await fs.writeFile(path.join(outDir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  if (browser === 'safari') {
    await fs.writeFile(
      path.join(outDir, 'docmatch-native.json'),
      JSON.stringify({
        siteURL: settings.siteURL,
        clientID: browserConfig.clientID,
        redirectURI: browserConfig.redirectURI,
        local,
      }) + '\n',
    );
    const nativeResources = path.join(root, 'safari/Web');
    await fs.rm(nativeResources, { recursive: true, force: true });
    await fs.cp(outDir, nativeResources, { recursive: true });
  }
  console.log(`Pacote ${browser}: ${outDir}`);
}
