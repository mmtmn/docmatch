import { GB, KB, MB } from './limits';
import { t } from './i18n';

const compact = new Intl.NumberFormat('pt-BR', { maximumFractionDigits: 1 });
const whole = new Intl.NumberFormat('pt-BR', { maximumFractionDigits: 0 });
const dateFormat = new Intl.DateTimeFormat('pt-BR', { dateStyle: 'long', timeZone: 'UTC' });
const shortDateFormat = new Intl.DateTimeFormat('pt-BR', { timeZone: 'UTC' });

function scaled(value: number): string {
  return value < 10 ? compact.format(value) : whole.format(value);
}

/** 1536 → "1,5 KB"; 52428800 → "50 MB". Binary units, pt-BR decimal comma. */
export function formatBytes(bytes: number): string {
  if (bytes >= GB) return t('files.size_gb', { n: scaled(bytes / GB) });
  if (bytes >= MB) return t('files.size_mb', { n: scaled(bytes / MB) });
  if (bytes >= KB) return t('files.size_kb', { n: scaled(bytes / KB) });
  return t('files.size_bytes', { n: whole.format(bytes) });
}

/** 75 → "01:15"; 3725 → "1:02:05". */
export function formatTimestamp(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = String(m).padStart(2, '0');
  const ss = String(s).padStart(2, '0');
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
}

/**
 * An approximate duration the same way on every client (testdata/duration/cases.json): minutes
 * rounded half up, at least one; under an hour, minutes ("12 minutos"); up to ten hours, hours
 * and minutes in steps of five ("1 h 20 min"); from ten hours on, whole hours ("11 h").
 */
export function formatDuration(seconds: number): string {
  const s = Number.isFinite(seconds) ? Math.max(0, Math.floor(seconds)) : 0;
  let minutes = Math.max(1, Math.floor((s + 30) / 60));
  if (minutes < 60) {
    return minutes === 1 ? t('duration.minute') : t('duration.minutes', { minutes });
  }
  minutes =
    minutes < 600 ? Math.floor((minutes + 2) / 5) * 5 : Math.floor((minutes + 30) / 60) * 60;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0
    ? t('duration.hours', { hours })
    : t('duration.hours_minutes', { hours, minutes: rest });
}

export function formatCurrency(amount: number, currency: string): string {
  return new Intl.NumberFormat('pt-BR', { style: 'currency', currency }).format(amount);
}

export function formatDate(value: string | Date): string {
  return dateFormat.format(typeof value === 'string' ? new Date(value) : value);
}

export function formatShortDate(value: string | Date): string {
  return shortDateFormat.format(typeof value === 'string' ? new Date(value) : value);
}

export function formatInteger(value: number): string {
  return whole.format(value);
}

/** Keeps the start and the extension visible: "relatorio-fechamento-2026-10.xlsx" → "relatorio-fe…10.xlsx". */
export function truncateMiddle(name: string, max = 28): string {
  if (name.length <= max) return name;
  const tail = Math.max(6, Math.floor(max / 3));
  const head = max - tail - 1;
  return `${name.slice(0, head)}…${name.slice(-tail)}`;
}
