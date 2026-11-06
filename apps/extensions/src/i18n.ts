import ptBR from '@docmatch/locales/pt-BR.json';
export function t(key: string, params: Record<string, string | number> = {}): string {
  const value = key
    .split('.')
    .reduce<unknown>(
      (object, part) =>
        object && typeof object === 'object'
          ? (object as Record<string, unknown>)[part]
          : undefined,
      ptBR,
    );
  if (typeof value !== 'string') throw new Error(`Missing locale: ${key}`);
  return value.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? `{${name}}`));
}
