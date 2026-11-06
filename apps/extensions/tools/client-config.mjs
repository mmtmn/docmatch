import fs from 'node:fs/promises';
const path = process.argv[2] ?? new URL('../local.config.json', import.meta.url);
const settings = JSON.parse(await fs.readFile(path, 'utf8'));
const clients = ['chrome', 'firefox', 'safari'].map((browser) => {
  const c = settings[browser];
  if (!c?.clientID || !c?.redirectURI)
    throw new Error(`Faltam clientID e redirectURI de ${browser}.`);
  return {
    id: c.clientID,
    name: `docmatch · ${browser[0].toUpperCase() + browser.slice(1)}`,
    redirect_uris: [c.redirectURI],
  };
});
process.stdout.write(JSON.stringify(clients) + '\n');
