import { t } from './i18n';

export function errorMessage(cause: unknown): string {
  return cause instanceof Error ? cause.message : t('errors.unknown');
}

export const projectDate = (value: string) =>
  new Intl.DateTimeFormat('pt-BR', { dateStyle: 'short', timeStyle: 'short' }).format(
    new Date(value),
  );
