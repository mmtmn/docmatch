#!/usr/bin/env node
// Build-time i18n check: every key used in t('…'), tl('…') and tf('…') (TS, TSX and Astro files)
// must exist in packages/locales/pt-BR.json with the right shape. Missing keys fail the build.
// Keys never referenced anywhere in src/ are listed as a warning so stale copy gets removed.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const appRoot = join(here, '..');
const localesFile = join(appRoot, '..', '..', 'packages', 'locales', 'pt-BR.json');
const srcDir = join(appRoot, 'src');

const SOURCE_EXT = /\.(ts|tsx|mts|js|mjs|astro)$/;
// Unit tests may reference deliberately missing keys.
const TEST_FILE = /\.test\.[a-z]+$/;
// t('key') / tl("key") / tf(`key`) with a static key (no `${}`).
const CALL = /\b(t|tl|tf)\(\s*(['"`])([^'"`$]+)\2/g;
// Copy written ahead of the screens that will use it (plan phases: auth, confirm dialogs). These
// keys are reported separately so the "unused" warning keeps pointing at stale copy only; remove
// an entry here as soon as its screen lands.
export const RESERVED_KEYS = [
  'auth.',
  // Used by the Android and iOS apps (apps/android, apps/ios), not by the web.
  'mobile.',
  // Used by the terminal installer (tools/installer), not by the web.
  'installer.',
  'common.yes',
  'common.no',
];

function walk(dir, out = []) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) walk(full, out);
    else if (SOURCE_EXT.test(entry) && !TEST_FILE.test(entry)) out.push(full);
  }
  return out;
}

function lookup(root, path) {
  let node = root;
  for (const segment of path.split('.')) {
    if (node === null || typeof node !== 'object') return undefined;
    node = node[segment];
  }
  return node;
}

function leaves(node, prefix = '', out = []) {
  if (typeof node === 'string') {
    out.push(prefix);
  } else if (Array.isArray(node)) {
    out.push(prefix);
  } else if (node && typeof node === 'object') {
    for (const [key, value] of Object.entries(node)) {
      leaves(value, prefix ? `${prefix}.${key}` : key, out);
    }
  }
  return out;
}

function shapeOk(fn, value) {
  if (fn === 't') return typeof value === 'string';
  if (fn === 'tl') return Array.isArray(value) && value.every((item) => typeof item === 'string');
  if (fn === 'tf') {
    return Array.isArray(value) && value.every((item) => item && typeof item === 'object');
  }
  return false;
}

/** A reserved entry is a full key or a prefix ending in "." covering a whole screen. */
function isReserved(key, reserved) {
  return reserved.some((entry) => (entry.endsWith('.') ? key.startsWith(entry) : key === entry));
}

export function checkSources({ messages, files, reserved = RESERVED_KEYS }) {
  const problems = [];
  const used = new Set();
  const corpus = [];
  for (const { path, text } of files) {
    corpus.push(text);
    for (const match of text.matchAll(CALL)) {
      const [, fn, , key] = match;
      used.add(key);
      const value = lookup(messages, key);
      if (value === undefined) {
        problems.push(`${path}: chave "${key}" não existe em pt-BR.json`);
      } else if (!shapeOk(fn, value)) {
        problems.push(`${path}: chave "${key}" não tem o formato esperado por ${fn}()`);
      }
    }
  }
  const all = corpus.join('\n');
  const idle = leaves(messages).filter((key) => {
    if (used.has(key)) return false;
    // Keys referenced as plain string literals (lookup tables such as KIND_LABEL_KEY) count.
    if (all.includes(`'${key}'`) || all.includes(`"${key}"`)) return false;
    // A list used through tl()/tf() covers its children.
    return ![...used].some((u) => key.startsWith(`${u}.`));
  });
  const unused = idle.filter((key) => !isReserved(key, reserved));
  const reservedUnused = idle.filter((key) => isReserved(key, reserved));
  // A reserved entry whose keys are all in use (or gone) is stale and should be removed.
  const staleReserved = reserved.filter(
    (entry) => !reservedUnused.some((key) => isReserved(key, [entry])),
  );
  return { problems, unused, reserved: reservedUnused, staleReserved, usedCount: used.size };
}

function main() {
  const messages = JSON.parse(readFileSync(localesFile, 'utf8'));
  const files = walk(srcDir).map((path) => ({
    path: relative(appRoot, path),
    text: readFileSync(path, 'utf8'),
  }));
  const { problems, unused, reserved, staleReserved, usedCount } = checkSources({
    messages,
    files,
  });
  if (reserved.length > 0) {
    console.log(`i18n: ${reserved.length} chave(s) reservadas para telas futuras (RESERVED_KEYS).`);
  }
  if (staleReserved.length > 0) {
    console.warn(
      `i18n: entradas de RESERVED_KEYS já em uso ou inexistentes: ${staleReserved.join(', ')}`,
    );
  }
  if (unused.length > 0) {
    console.warn(`i18n: ${unused.length} chave(s) sem uso em src/: ${unused.join(', ')}`);
  }
  if (problems.length > 0) {
    console.error(problems.map((line) => `i18n: ${line}`).join('\n'));
    process.exit(1);
  }
  console.log(`i18n: ${usedCount} chave(s) verificadas em ${files.length} arquivo(s).`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  main();
}
