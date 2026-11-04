import { fileURLToPath } from 'node:url';
import type { Plugin } from 'vite';
import { validateLimits, type Limits } from './limits-schema';
import { loadYamlModule, yamlVirtual } from './yaml-virtual';

export const LIMITS_FILE = fileURLToPath(
  new URL('../../../packages/contracts/limits.yaml', import.meta.url),
);

/** `import limits from 'virtual:limits'` → the parsed, validated limits.yaml. */
export function limitsPlugin(file: string = LIMITS_FILE): Plugin {
  return yamlVirtual<Limits>({ id: 'virtual:limits', file, validate: validateLimits });
}

/** Direct loader for Node-side scripts and tests. */
export function loadLimits(file: string = LIMITS_FILE): Limits {
  return loadYamlModule<Limits>({ id: 'virtual:limits', file, validate: validateLimits });
}
