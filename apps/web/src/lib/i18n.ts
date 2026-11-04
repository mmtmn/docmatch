// Single entry point for every visible string. The JSON lives in packages/locales so Android and
// iOS can read the same file; the key union below gives compile-time checking in TS/TSX and the
// script tools/i18n-check.mjs repeats the check at build time for .astro templates.
import messages from '@docmatch/locales/pt-BR.json';

type Messages = typeof messages;

type Join<P extends string, K extends string> = P extends '' ? K : `${P}.${K}`;

/** Dot-separated paths whose leaf is a string. */
type StringLeaves<T, P extends string = ''> = T extends string
  ? P
  : T extends readonly unknown[]
    ? never
    : T extends object
      ? { [K in keyof T & string]: StringLeaves<T[K], Join<P, K>> }[keyof T & string]
      : never;

/** Dot-separated paths whose leaf is a list of strings. */
type ListLeaves<T, P extends string = ''> = T extends readonly string[]
  ? P
  : T extends readonly unknown[]
    ? never
    : T extends object
      ? { [K in keyof T & string]: ListLeaves<T[K], Join<P, K>> }[keyof T & string]
      : never;

/** Dot-separated paths whose leaf is a list of records (steps, FAQ items, legal sections). */
type TableLeaves<T, P extends string = ''> = T extends readonly string[]
  ? never
  : T extends readonly object[]
    ? P
    : T extends object
      ? { [K in keyof T & string]: TableLeaves<T[K], Join<P, K>> }[keyof T & string]
      : never;

type PathValue<T, P extends string> = P extends `${infer Head}.${infer Rest}`
  ? Head extends keyof T
    ? PathValue<T[Head], Rest>
    : never
  : P extends keyof T
    ? T[P]
    : never;

export type MessageKey = StringLeaves<Messages>;
export type ListKey = ListLeaves<Messages>;
export type TableKey = TableLeaves<Messages>;
export type Vars = Record<string, string | number>;

const numberFormat = new Intl.NumberFormat('pt-BR');

function lookup(path: string): unknown {
  let node: unknown = messages;
  for (const segment of path.split('.')) {
    if (node === null || typeof node !== 'object') return undefined;
    node = (node as Record<string, unknown>)[segment];
  }
  return node;
}

function report(key: string, expected: string): void {
  if (import.meta.env.DEV) {
    // Surfaces typos immediately in dev; the build-time check makes this unreachable in prod.
    console.warn(`i18n: chave "${key}" não é ${expected} em pt-BR.json`);
  }
}

/** Replaces `{name}` with vars[name]; numbers are formatted in pt-BR. Unknown names stay as-is. */
export function interpolate(template: string, vars: Vars): string {
  return template.replace(/\{([A-Za-z0-9_]+)\}/g, (match, name: string) => {
    if (!Object.hasOwn(vars, name)) return match;
    const value = vars[name];
    return typeof value === 'number' ? numberFormat.format(value) : String(value);
  });
}

/** Translates a string leaf. Missing keys render as `[key]` so they are visible, never blank. */
export function t(key: MessageKey, vars?: Vars): string {
  const value = lookup(key);
  if (typeof value !== 'string') {
    report(key, 'uma string');
    return `[${key}]`;
  }
  return vars ? interpolate(value, vars) : value;
}

/** Translates a list of strings. */
export function tl(key: ListKey): readonly string[] {
  const value = lookup(key);
  if (!Array.isArray(value) || !value.every((item) => typeof item === 'string')) {
    report(key, 'uma lista de strings');
    return [];
  }
  return value;
}

/** Translates a list of records (each record is a flat object of strings or string lists). */
export function tf<K extends TableKey>(key: K): PathValue<Messages, K> {
  const value = lookup(key);
  if (!Array.isArray(value)) {
    report(key, 'uma lista de objetos');
    return [] as unknown as PathValue<Messages, K>;
  }
  return value as PathValue<Messages, K>;
}

/** True when the key resolves to a string leaf; used by tests and by the lint script. */
export function hasMessage(key: string): boolean {
  return typeof lookup(key) === 'string';
}

export const locale = 'pt-BR';
