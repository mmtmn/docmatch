import { createHash, randomUUID } from 'node:crypto';
import { mkdir, lstat, readFile, writeFile, rename, rm, chmod } from 'node:fs/promises';
import { join } from 'node:path';

const maxBytes = 64 * 1024 * 1024;
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');

export function assetFor(release, platform, arch) {
  const os = { linux: 'linux', darwin: 'darwin', win32: 'windows' }[platform];
  const cpu = { x64: 'amd64', arm64: 'arm64' }[arch];
  const asset = release.assets?.[`${os}/${cpu}`];
  if (!os || !cpu || !asset) throw new Error(`Plataforma não suportada: ${platform}/${arch}`);
  if (!/^\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/.test(release.version) ||
      !/^[a-f0-9]{64}$/.test(asset.sha256)) throw new Error('Manifesto de release inválido.');
  const url = new URL(asset.url);
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash)
    throw new Error('O binário precisa de uma origem HTTPS sem credenciais.');
  return { ...asset, filename: `docmatch-${asset.sha256}${os === 'windows' ? '.exe' : ''}` };
}

export async function ensureBinary({ release, platform, arch, cache, fetcher = fetch }) {
  const asset = assetFor(release, platform, arch);
  const directory = join(cache, 'docmatch', release.version);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const target = join(directory, asset.filename);
  try {
    const stat = await lstat(target);
    if (stat.isFile() && stat.size <= maxBytes && digest(await readFile(target)) === asset.sha256) return target;
    // Never follow a replaced cache entry or execute a modified binary.
    await rm(target);
  } catch (error) { if (error.code !== 'ENOENT') throw error; }

  const response = await fetcher(asset.url, { redirect: 'error', signal: AbortSignal.timeout(120_000) });
  if (!response.ok || !response.body) throw new Error(`Download recusado (${response.status}). Tente novamente.`);
  if (Number(response.headers.get('content-length')) > maxBytes) throw new Error('Binário excede o limite de tamanho.');
  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    if (size > maxBytes) throw new Error('Binário excede o limite de tamanho.');
    chunks.push(chunk);
  }
  const bytes = Buffer.concat(chunks);
  if (digest(bytes) !== asset.sha256) throw new Error('Checksum inválido. O download não será executado.');
  const temporary = join(directory, `.${randomUUID()}.tmp`);
  try {
    await writeFile(temporary, bytes, { mode: 0o700, flag: 'wx' });
    await chmod(temporary, 0o700);
    await rename(temporary, target);
  } finally { await rm(temporary, { force: true }); }
  return target;
}
