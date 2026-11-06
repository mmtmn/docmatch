import { build } from 'vite';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const local = process.argv.includes('--local');
const configPath = local
  ? path.join(root, 'local.config.json')
  : process.env.DM_EXTENSION_CONFIG_FILE;
if (!configPath)
  throw new Error('Defina DM_EXTENSION_CONFIG_FILE com as origens e IDs de produção.');
const settings = JSON.parse(await fs.readFile(configPath, 'utf8'));
for (const field of ['apiURL', 'siteURL']) {
  const url = new URL(settings[field]);
  if (
    local
      ? !['localhost', '127.0.0.1'].includes(url.hostname)
      : url.protocol !== 'https:' || ['localhost', '127.0.0.1'].includes(url.hostname)
  )
    throw new Error(`Origem inválida: ${field}`);
  if (url.pathname !== '/' || url.search || url.hash || url.username || url.password)
    throw new Error(`Use apenas a origem em ${field}`);
}
const hostPattern = (value) => {
  const url = new URL(value);
  if (
    !local &&
    (url.protocol !== 'https:' || /^(localhost|127\.|10\.|192\.168\.|\[::1\])/.test(url.hostname))
  )
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
for (const browser of ['chrome', 'firefox', 'safari']) {
  const browserConfig = settings[browser];
  if (!browserConfig?.clientID) throw new Error(`Falta clientID de ${browser}`);
  const outDir = path.join(root, 'dist', local ? 'local' : 'production', browser);
  const define = {
    __EXTENSION_CONFIG__: JSON.stringify({
      apiURL: settings.apiURL,
      siteURL: settings.siteURL,
      clientID: browserConfig.clientID,
      browser,
      local,
    }),
  };
  await build({
    configFile: false,
    root,
    base: './',
    define,
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
      gecko: { id: browserConfig.id, strict_min_version: '115.0' },
    };
  }
  await fs.copyFile(
    path.join(root, '../web/public/brand/docmatch-avatar-512.png'),
    path.join(outDir, 'icon.png'),
  );
  await fs.writeFile(path.join(outDir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  console.log(`Pacote ${browser}: ${outDir}`);
}
