import { describe, expect, it } from 'vitest';
import {
  countChars,
  counterThreshold,
  fileCaps,
  limits,
  MB,
  resolveFileCaps,
  textLimits,
} from './limits';

describe('limits (virtual:limits)', () => {
  it('exposes the numbers from packages/contracts/limits.yaml', () => {
    expect(typeof limits.version).toBe('number');
    expect(textLimits.rules_text).toBeGreaterThan(0);
    expect(textLimits.rule_picker_search).toBeGreaterThan(0);
    expect(fileCaps.documentFileBytes).toBe(limits.files.document_file_max_mb * MB);
    expect(fileCaps.filesPerRun).toBe(limits.ids.files_per_run_hard_max);
  });

  it('counter threshold is 80% rounded up', () => {
    expect(counterThreshold(100)).toBe(80);
    expect(counterThreshold(33)).toBe(27);
  });

  it('counts code points after NFC, not UTF-16 units', () => {
    expect(countChars('é')).toBe(1);
    expect(countChars('😀')).toBe(1);
    expect(countChars('abc')).toBe(3);
  });

  it('plan caps can only tighten the hard caps', () => {
    const caps = resolveFileCaps({ documentFileBytes: 1, filesPerRun: 10_000, mediaRunBytes: 0 });
    expect(caps.documentFileBytes).toBe(1);
    expect(caps.filesPerRun).toBe(fileCaps.filesPerRun);
    expect(caps.mediaRunBytes).toBe(fileCaps.mediaRunBytes);
  });
});
