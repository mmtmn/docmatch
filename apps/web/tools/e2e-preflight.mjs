#!/usr/bin/env node
// Prints the browsers that can actually run a page here (comma-separated). `make e2e` skips the
// suite, loudly, when none can: browsers need system libraries that we never install with sudo.
// Launching is not enough (WebKit starts, renders data: URLs, and then crashes its web process on
// the first HTTP navigation when libraries are missing), so the probe serves a page over HTTP
// from this process and evaluates inside it.
import { createServer } from 'node:http';
import { chromium, firefox, webkit } from '@playwright/test';

const wanted = process.argv.slice(2).length > 0 ? process.argv.slice(2) : ['chromium'];
const engines = { chromium, firefox, webkit };
const launchable = [];

const server = createServer((_request, response) => {
  response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
  response.end('<!doctype html><title>ok</title><p>ok</p>');
});
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const { port } = server.address();

for (const name of wanted) {
  const engine = engines[name];
  if (!engine) continue;
  let browser = null;
  try {
    browser = await engine.launch();
    const page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${port}/`, { timeout: 15_000 });
    const title = await page.evaluate(() => document.title);
    if (title !== 'ok') throw new Error('página de teste não renderizou');
    launchable.push(name);
  } catch (error) {
    console.error(`e2e: ${name} não pôde abrir uma página: ${String(error).split('\n')[0]}`);
  } finally {
    await browser?.close().catch(() => {});
  }
}

server.close();
console.log(launchable.join(','));
process.exit(launchable.length > 0 ? 0 : 1);
