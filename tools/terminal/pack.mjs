// Produces a standalone npm package from the six verified installer release artifacts.
import { readFile, writeFile, mkdir, cp } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const [releaseDir, baseURL, outDir] = process.argv.slice(2);
if (!releaseDir || !baseURL || !outDir) throw new Error('Uso: node pack.mjs RELEASE_DIR HTTPS_BASE OUT_DIR');
const base = new URL(baseURL);
if (base.protocol !== 'https:' || base.username || base.password || base.search || base.hash) throw new Error('Base HTTPS inválida');
const version = (await readFile(join(releaseDir, 'VERSION.txt'), 'utf8')).trim();
if (!/^\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/.test(version)) throw new Error('Versão inválida');
const sums = new Map((await readFile(join(releaseDir, 'SHA256SUMS.txt'), 'utf8')).trim().split('\n').map((line) => {
  const [hash, file] = line.trim().split(/\s+/); return [file.replace(/^\*/, ''), hash];
}));
const assets = {};
for (const os of ['linux', 'darwin', 'windows']) for (const arch of ['amd64', 'arm64']) {
  const filename = `docmatch-${os}-${arch}.${os === 'windows' ? 'exe' : 'bin'}`;
  const actual = createHash('sha256').update(await readFile(join(releaseDir, filename))).digest('hex');
  if (sums.get(filename) !== actual) throw new Error(`Checksum divergente: ${filename}`);
  assets[`${os}/${arch}`] = { url: `${baseURL.replace(/\/$/, '')}/v${version}/${filename}`, sha256: actual };
}
const source = fileURLToPath(new URL('.', import.meta.url));
const output = resolve(outDir);
await mkdir(output, { recursive: true });
for (const name of ['bin', 'lib', 'README.md']) await cp(join(source, name), join(output, name), { recursive: true });
await cp(join(source, '../../LICENSE'), join(output, 'LICENSE'));
await writeFile(join(output, 'release.json'), JSON.stringify({ version, assets, packageURL: `${baseURL.replace(/\/$/, '')}/docmatch.tgz` }, null, 2) + '\n');
await writeFile(join(output, 'package.json'), JSON.stringify({
  name: 'docmatch', version, description: 'Documentos, modelos e configuração no terminal',
  type: 'module', license: 'AGPL-3.0-only', bin: { docmatch: 'bin/docmatch.mjs' },
  engines: { node: '>=20' }, files: ['bin', 'lib', 'release.json', 'README.md', 'LICENSE'],
  homepage: 'https://docmatch.com.br', repository: { type: 'git', url: 'git+https://github.com/mmtmn/docmatch.git', directory: 'tools/terminal' },
}, null, 2) + '\n');
execFileSync('npm', ['pack', '--ignore-scripts'], { cwd: output, stdio: 'inherit' });
