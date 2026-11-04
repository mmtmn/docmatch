import { describe, expect, it } from 'vitest';
import { csvCell, toCsv } from './csv';

describe('csv', () => {
  it('escapes separators, quotes and line breaks', () => {
    expect(csvCell('a;b')).toBe('"a;b"');
    expect(csvCell('diz "oi"')).toBe('"diz ""oi"""');
    expect(csvCell('linha\nnova')).toBe('"linha\nnova"');
    expect(csvCell('simples')).toBe('simples');
  });

  it('neutralises formula injection', () => {
    expect(csvCell('=1+1')).toBe("'=1+1");
    expect(csvCell('@cmd')).toBe("'@cmd");
  });

  it('writes numbers with a decimal comma and no grouping', () => {
    expect(csvCell(1234.5)).toBe('1234,5');
    expect(csvCell(-9.9)).toBe('-9,9');
  });

  it('produces a BOM, semicolon separators and CRLF', () => {
    const out = toCsv(['a', 'b'], [['x', 1]]);
    expect(out).toBe('﻿a;b\r\nx;1\r\n');
  });
});
