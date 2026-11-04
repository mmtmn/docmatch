// @vitest-environment node
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Plugin } from 'vite';
import { describe, expect, it, vi } from 'vitest';
import { limitsPlugin, loadLimits } from './limits';
import { validateLimits } from './limits-schema';
import { loadPricing, pricingPlugin } from './pricing';
import { validatePricing } from './pricing-schema';
import { renderYamlModule } from './yaml-virtual';

type Hook = (this: { addWatchFile: (file: string) => void }, id: string) => unknown;

function hook(plugin: Plugin, name: 'resolveId' | 'load'): Hook {
  const value = plugin[name] as unknown;
  if (typeof value === 'function') return value as Hook;
  return (value as { handler: Hook }).handler;
}

describe('virtual:limits', () => {
  it('loads and validates the real limits.yaml', () => {
    const limits = loadLimits();
    expect(limits.version).toBe(1);
    expect(limits.text.rules_text).toBeGreaterThan(0);
    expect(limits.files.document_file_max_mb).toBeGreaterThan(0);
    expect(limits.ids.files_per_run_hard_max).toBeGreaterThan(0);
  });

  it('fails fast when a limit is missing or not a number', () => {
    expect(() =>
      validateLimits({ version: 1, text: {}, files: {}, http: {}, ids: {} }, 'x.yaml'),
    ).toThrow(/text\.rules_text/);
    expect(() => validateLimits('nope', 'x.yaml')).toThrow(/mapa/);
    const limits = loadLimits() as unknown as { text: Record<string, unknown> };
    const broken = structuredClone(limits);
    broken.text['rules_text'] = 'muitos';
    expect(() => validateLimits(broken, 'x.yaml')).toThrow(/rules_text/);
  });

  it('resolves and renders the virtual module with named exports', () => {
    const plugin = limitsPlugin();
    expect(hook(plugin, 'resolveId').call({ addWatchFile: vi.fn() }, 'virtual:limits')).toBe(
      '\0virtual:limits',
    );
    expect(hook(plugin, 'resolveId').call({ addWatchFile: vi.fn() }, 'other')).toBeNull();
    const addWatchFile = vi.fn();
    const code = hook(plugin, 'load').call({ addWatchFile }, '\0virtual:limits') as string;
    expect(addWatchFile).toHaveBeenCalledTimes(1);
    expect(code).toContain('export default data');
    expect(code).toContain('export const text = data["text"]');
    expect(code).toMatch(/"rules_text":\d+/);
  });

  it('reads a custom file and reports its path on error', () => {
    const dir = mkdtempSync(join(tmpdir(), 'docmatch-limits-'));
    const file = join(dir, 'limits.yaml');
    writeFileSync(file, 'version: 1\ntext:\n  rules_text: 10\n');
    expect(() => loadLimits(file)).toThrow(new RegExp(file.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  });

  it('renderYamlModule skips keys that are not identifiers', () => {
    const code = renderYamlModule({ ok: 1, 'not-ok': 2 });
    expect(code).toContain('export const ok');
    expect(code).not.toContain('not-ok = ');
  });
});

describe('virtual:pricing', () => {
  it('loads and validates config/pricing.yaml', () => {
    const pricing = loadPricing();
    expect(pricing.plans.starter.price_usd).toBeGreaterThan(0);
    expect(pricing.plans.business.api_lanes).toContain('api_priority');
    expect(pricing.lanes.app).toBe(1);
  });

  it('fails fast on an invalid plan', () => {
    const pricing = structuredClone(loadPricing()) as unknown as {
      plans: Record<string, Record<string, unknown>>;
    };
    pricing.plans['pro']!['api_lanes'] = ['fila-inexistente'];
    expect(() => validatePricing(pricing, 'pricing.yaml')).toThrow(/plans\.pro\.api_lanes/);
  });

  it('exposes only the public subset (rates and estimates never reach the browser)', () => {
    const input = structuredClone(loadPricing()) as unknown as Record<string, unknown>;
    input['rates'] = { target_margin: 0.3 };
    input['estimates'] = { new_tool_usd: 1 };
    (input['plans'] as Record<string, Record<string, unknown>>)['pro']!['internal_note'] = 'x';
    const pricing = validatePricing(input, 'pricing.yaml') as unknown as Record<string, unknown>;
    expect(Object.keys(pricing).sort()).toEqual(
      ['lanes', 'min_tokens_per_run', 'plans', 'quote_ttl_minutes', 'rollover', 'token_usd'].sort(),
    );
    expect(pricing['plans']).not.toHaveProperty('pro.internal_note');
    expect(loadPricing()).not.toHaveProperty('rates');
    expect(loadPricing()).not.toHaveProperty('estimates');
  });

  it('resolves the virtual id', () => {
    const plugin = pricingPlugin();
    expect(hook(plugin, 'resolveId').call({ addWatchFile: vi.fn() }, 'virtual:pricing')).toBe(
      '\0virtual:pricing',
    );
  });
});
