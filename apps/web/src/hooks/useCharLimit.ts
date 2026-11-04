import { useMemo } from 'react';
import { t } from '../lib/i18n';
import { countChars, counterThreshold } from '../lib/limits';

export interface CharLimitState {
  count: number;
  max: number;
  /** Visible from 80% of the limit. */
  showCounter: boolean;
  atLimit: boolean;
  /** Counter text, or the "limit reached" message once `atLimit`. */
  message: string;
}

export function charLimitState(value: string, max: number): CharLimitState {
  const count = countChars(value);
  const atLimit = count >= max;
  const showCounter = count >= counterThreshold(max);
  const message = atLimit
    ? t('form.counter_limit', { max })
    : showCounter
      ? t('form.counter', { count, max })
      : '';
  return { count, max, showCounter, atLimit, message };
}

export function useCharLimit(value: string, max: number): CharLimitState {
  return useMemo(() => charLimitState(value, max), [value, max]);
}
