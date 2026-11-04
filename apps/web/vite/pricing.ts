import { fileURLToPath } from 'node:url';
import type { Plugin } from 'vite';
import { validatePricing, type Pricing } from './pricing-schema';
import { loadYamlModule, yamlVirtual } from './yaml-virtual';

export const PRICING_FILE = fileURLToPath(new URL('../../../config/pricing.yaml', import.meta.url));

/** `import pricing from 'virtual:pricing'` → the parsed, validated config/pricing.yaml. */
export function pricingPlugin(file: string = PRICING_FILE): Plugin {
  return yamlVirtual<Pricing>({ id: 'virtual:pricing', file, validate: validatePricing });
}

export function loadPricing(file: string = PRICING_FILE): Pricing {
  return loadYamlModule<Pricing>({ id: 'virtual:pricing', file, validate: validatePricing });
}
