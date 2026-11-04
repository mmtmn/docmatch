#!/usr/bin/env node
// Prints the browsers that can actually launch here (one per line). `make e2e` skips the suite,
// loudly, when none can: browsers need system libraries that we never install with sudo.
import { chromium, firefox, webkit } from '@playwright/test';

const wanted = process.argv.slice(2).length > 0 ? process.argv.slice(2) : ['chromium'];
const engines = { chromium, firefox, webkit };
const launchable = [];

for (const name of wanted) {
  const engine = engines[name];
  if (!engine) continue;
  try {
    const browser = await engine.launch();
    await browser.close();
    launchable.push(name);
  } catch (error) {
    console.error(`e2e: ${name} não pôde abrir: ${String(error).split('\n')[0]}`);
  }
}

console.log(launchable.join(','));
process.exit(launchable.length > 0 ? 0 : 1);
