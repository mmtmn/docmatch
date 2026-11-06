import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { assetFor, ensureBinary } from '../lib/launcher.mjs';

const bytes = Buffer.from('fixture executable');
const sha256 = createHash('sha256').update(bytes).digest('hex');
const release = { version: '0.1.0', assets: Object.fromEntries(['linux','darwin','windows'].flatMap(os =>
  ['amd64','arm64'].map(arch => [`${os}/${arch}`, { url: `https://release.example.invalid/${os}-${arch}`, sha256 }])))};

test('all six platforms resolve and unsafe manifests fail closed', () => {
  for (const platform of ['linux','darwin','win32']) for (const arch of ['x64','arm64'])
    assert.equal(assetFor(release, platform, arch).sha256, sha256);
  assert.throws(() => assetFor(release,'freebsd','x64'));
  for (const url of ['http://example.invalid/x', 'https://user:secret@example.invalid/x'])
    assert.throws(() => assetFor({ ...release, assets: { 'linux/amd64': { url, sha256 }}},'linux','x64'));
  assert.throws(() => assetFor({ ...release, version: '../../escape' },'linux','x64'));
});

test('verified binary is cached, corrupt cache is replaced, no redirects or install scripts', async t => {
  const cache = await mkdtemp(join(tmpdir(),'docmatch-npm-')); t.after(() => rm(cache,{recursive:true,force:true}));
  let downloads = 0;
  const fetcher = async (_, opts) => { downloads++; assert.equal(opts.redirect,'error'); return new Response(bytes); };
  const opts = { release, platform:'linux', arch:'x64', cache, fetcher };
  const file = await ensureBinary(opts);
  assert.deepEqual(await readFile(file),bytes);
  assert.equal(await ensureBinary(opts),file); assert.equal(downloads,1);
  await writeFile(file,'tampered'); await ensureBinary(opts); assert.equal(downloads,2);
});

test('tampered and oversized downloads never become executable', async t => {
  const cache = await mkdtemp(join(tmpdir(),'docmatch-npm-')); t.after(() => rm(cache,{recursive:true,force:true}));
  const opts = { release, platform:'linux', arch:'x64', cache };
  await assert.rejects(ensureBinary({ ...opts, fetcher:async () => new Response('wrong') }),/Checksum/);
  await assert.rejects(ensureBinary({ ...opts, fetcher:async () => new Response(bytes,{headers:{'content-length':'999999999'}}) }),/tamanho/);
  await assert.rejects(ensureBinary({ ...opts, fetcher:async () => new Response(null,{status:404}) }),/404/);
});
