#!/usr/bin/env node
import { readFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { spawn } from 'node:child_process';
import { ensureBinary } from '../lib/launcher.mjs';

try {
  const release = JSON.parse(await readFile(new URL('../release.json', import.meta.url), 'utf8'));
  const cache = process.platform === 'win32'
    ? (process.env.LOCALAPPDATA || join(homedir(), 'AppData', 'Local'))
    : (process.env.XDG_CACHE_HOME || join(homedir(), '.cache'));
  const binary = await ensureBinary({ release, platform: process.platform, arch: process.arch, cache });
  // npm updates the package and its pinned checksum; do not mutate an npm-managed binary.
  if (process.argv[2] === 'update') {
    console.log(`Atualize com: npm install -g ${release.packageURL ? '--allow-remote=root ' + release.packageURL : 'docmatch@latest'}`);
  } else {
    const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit', shell: false });
    const signals = ['SIGINT', 'SIGTERM'];
    const handlers = signals.map((signal) => () => child.kill(signal));
    signals.forEach((signal, i) => process.on(signal, handlers[i]));
    child.on('error', (error) => { console.error(`docmatch: ${error.message}`); process.exitCode = 1; });
    child.on('exit', (code, signal) => {
      signals.forEach((name, i) => process.off(name, handlers[i]));
      process.exitCode = code ?? (signal === 'SIGINT' ? 130 : 1);
    });
  }
} catch (error) {
  console.error(`docmatch: ${error.message}`);
  process.exitCode = 1;
}
