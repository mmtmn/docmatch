import { describe, expect, it } from 'vitest';
import {
  formatBytes,
  formatCurrency,
  formatShortDate,
  formatTimestamp,
  truncateMiddle,
} from './format';

describe('format', () => {
  it('formats bytes with binary units and a pt-BR decimal comma', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1,5 KB');
    expect(formatBytes(50 * 1024 * 1024)).toBe('50 MB');
    expect(formatBytes(4 * 1024 ** 3)).toBe('4 GB');
  });

  it('formats timestamps as mm:ss or h:mm:ss', () => {
    expect(formatTimestamp(0)).toBe('00:00');
    expect(formatTimestamp(75)).toBe('01:15');
    expect(formatTimestamp(3725)).toBe('1:02:05');
  });

  it('formats currency and dates in pt-BR', () => {
    expect(formatCurrency(1234.5, 'BRL').replace(/\u00a0/g, ' ')).toBe('R$ 1.234,50');
    expect(formatShortDate('2026-10-12')).toBe('12/10/2026');
  });

  it('truncates long names keeping the extension', () => {
    const name = 'relatorio-fechamento-outubro-2026-final.xlsx';
    const short = truncateMiddle(name, 24);
    expect(short.length).toBeLessThanOrEqual(24);
    expect(short.endsWith('.xlsx')).toBe(true);
    expect(truncateMiddle('curto.csv')).toBe('curto.csv');
  });
});
