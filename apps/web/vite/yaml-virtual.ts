// Tiny Vite plugin: exposes a YAML file as a virtual ES module, parsed and validated at build time.
// Used for `virtual:limits` (packages/contracts/limits.yaml) and `virtual:pricing`
// (config/pricing.yaml) so no number from those files is ever retyped in the web app.
import { readFileSync } from 'node:fs';
import { parse } from 'yaml';
import type { Plugin } from 'vite';

export interface YamlVirtualOptions<T extends object> {
  /** Import specifier, e.g. `virtual:limits`. */
  id: string;
  /** Absolute path of the YAML file. */
  file: string;
  /** Validates the parsed document and returns the typed value; throws to fail the build. */
  validate: (data: unknown, source: string) => T;
}

const IDENTIFIER = /^[A-Za-z_$][A-Za-z0-9_$]*$/;

export function loadYamlModule<T extends object>(options: YamlVirtualOptions<T>): T {
  const raw = readFileSync(options.file, 'utf8');
  return options.validate(parse(raw), options.file);
}

/** Renders the module source: a default export plus one named export per top-level key. */
export function renderYamlModule(data: object): string {
  const json = JSON.stringify(data);
  const named = Object.keys(data)
    .filter((key) => IDENTIFIER.test(key))
    .map((key) => `export const ${key} = data[${JSON.stringify(key)}];`)
    .join('\n');
  return `const data = ${json};\nexport default data;\n${named}\n`;
}

export function yamlVirtual<T extends object>(options: YamlVirtualOptions<T>): Plugin {
  const resolvedId = `\0${options.id}`;
  return {
    name: `docmatch:${options.id}`,
    resolveId(id) {
      return id === options.id ? resolvedId : null;
    },
    load(id) {
      if (id !== resolvedId) return null;
      this.addWatchFile(options.file);
      return renderYamlModule(loadYamlModule(options));
    },
  };
}
