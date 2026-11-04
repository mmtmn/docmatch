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
